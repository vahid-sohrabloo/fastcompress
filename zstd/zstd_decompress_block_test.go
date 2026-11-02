package zstd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
)

// newTestDctx returns a dctx reset for a frame whose blocks are at most
// bsMax bytes.
func newTestDctx(bsMax int) *dctx {
	d := new(dctx)
	d.decompressBegin()
	d.fParams.blockSizeMax = uint32(bsMax)
	return d
}

// decodedLits returns the literals decodeLiteralsBlock left in d.
func decodedLits(d *dctx) []byte {
	if d.litBufferLocation == split {
		out := append([]byte(nil), d.litBuf[d.litPtr:d.litBufferEnd]...)
		return append(out, d.litExtraBuffer[:litBufferExtraSize]...)
	}
	return d.litBuf[d.litPtr : d.litPtr+d.litSize]
}

// testRepOffset turns an encoder offBase into the absolute offset and
// updates rep, as the encoder does. Test-local on purpose: it is the
// oracle for decodeSequence's repcode handling.
// C: zstd_compress_internal.h:ZSTD_updateRep (OFFBASE_TO_OFFSET, repcodes)
func testRepOffset(rep *[repNum]uint32, offBase uint32, ll0 bool) int {
	if offBase > repNum {
		off := offBase - repNum
		rep[2], rep[1], rep[0] = rep[1], rep[0], off
		return int(off)
	}
	repCode := offBase - 1
	if ll0 {
		repCode++
	}
	if repCode == 0 {
		return int(rep[0])
	}
	cur := rep[0] - 1
	if repCode != repNum {
		cur = rep[repCode]
	}
	if repCode >= 2 {
		rep[2] = rep[1]
	}
	rep[1], rep[0] = rep[0], cur
	return int(cur)
}

// TestDecodeSeqsCref decodes the literals and sequences of every compressed
// block of C's frames and compares them with C's seqStore (`cref seqs`).
func TestDecodeSeqsCref(t *testing.T) {
	inputs := creftest.Inputs(t)
	names := make([]string, 0, len(inputs))
	for n, b := range inputs {
		if len(b) <= 1<<20 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, level := range []int{1, 3, 7, 11} {
		for _, name := range names {
			src := inputs[name]
			t.Run("L"+strconv.Itoa(level)+"/"+name, func(t *testing.T) {
				t.Parallel()
				testDecodeSeqs(t, src, level)
			})
		}
	}
}

func testDecodeSeqs(t *testing.T, src []byte, level int) {
	lv := strconv.Itoa(level)
	frame := creftest.Run(t, src, "compress", "-level", lv, "-pledged", "1", "-checksum", "0")
	cblocks := parseCrefSeqs(t, creftest.Run(t, src, "seqs", "-level", lv))

	var zfh frameHeader
	if need, e := getFrameHeaderAdvanced(&zfh, frame); need != 0 || e != 0 {
		t.Fatalf("frame header: need %d err %d", need, e)
	}
	d := new(dctx)
	d.decompressBegin()
	d.fParams = zfh
	rep := repStartValue // the encoder's confirmed repcodes
	out := make([]byte, len(src))
	ip, pos := int(zfh.headerSize), 0
	nCompressed, nSeqs := 0, 0
	for i := 0; ; i++ {
		cSize, bp, e := getcBlockSize(frame[ip:])
		if e != 0 {
			t.Fatalf("block %d: getcBlockSize: %d", i, e)
		}
		ip += blockHeaderSize
		block := frame[ip : ip+cSize]
		ip += cSize
		if i >= len(cblocks) {
			t.Fatalf("frame has more blocks than cref seqs dumped (%d)", len(cblocks))
		}
		cb := cblocks[i]
		typ := map[int]string{btRaw: "raw", btRLE: "rle", btCompressed: "compressed"}[bp.blockType]
		if typ != cb.typ {
			t.Fatalf("block %d: frame has %s, cref seqs %s", i, typ, cb.typ)
		}
		if bp.blockType == btCompressed {
			nCompressed++
			nSeqs += len(cb.ss.sequences)
			checkBlockSeqs(t, i, d, block, out[pos:], cb.ss, &rep)
		}
		pos += cb.src
		if bp.lastBlock {
			if i != len(cblocks)-1 {
				t.Fatalf("frame has %d blocks, cref seqs dumped %d", i+1, len(cblocks))
			}
			break
		}
	}
	t.Logf("%d compressed blocks, %d sequences", nCompressed, nSeqs)
}

// checkBlockSeqs decodes one compressed block's literals and sequences
// (output space dst, as C's one-shot decoder gives it) and compares them
// with C's seqStore ss. rep holds the encoder's repcodes.
func checkBlockSeqs(t *testing.T, i int, d *dctx, block, dst []byte, ss *seqStore, rep *[repNum]uint32) {
	t.Helper()
	litCSize, e := d.decodeLiteralsBlock(block, dst, len(dst), notStreaming)
	if e != 0 {
		t.Fatalf("block %d: decodeLiteralsBlock: %d", i, e)
	}
	if got := decodedLits(d); !bytes.Equal(got, ss.literals) {
		t.Fatalf("block %d: literals differ (got %d, want %d bytes, location %d)", i, len(got), len(ss.literals), d.litBufferLocation)
	}
	nbSeq, hSize, e := decodeSeqHeaders(d, block[litCSize:])
	if e != 0 {
		t.Fatalf("block %d: decodeSeqHeaders: %d", i, e)
	}
	if nbSeq != len(ss.sequences) {
		t.Fatalf("block %d: nbSeq %d, want %d", i, nbSeq, len(ss.sequences))
	}
	if nbSeq == 0 {
		return
	}
	// C: ZSTD_decompressSequences_body prologue.
	d.fseEntropy = true
	var st seqState
	for j := range st.prevOffset {
		st.prevOffset[j] = int(d.entropy.rep[j])
	}
	if _, err := st.dStream.Init(block[litCSize+hSize:]); err != nil {
		t.Fatalf("block %d: DStream.Init: %v", i, err)
	}
	initFseState(&st.stateLL, &st.dStream, d.llTptr)
	initFseState(&st.stateOffb, &st.dStream, d.ofTptr)
	initFseState(&st.stateML, &st.dStream, d.mlTptr)
	for n, sd := range ss.sequences {
		want := seq{litLength: int(sd.litLength), matchLength: int(sd.mlBase) + minMatch}
		if ss.longLengthType != lltNone && uint32(n) == ss.longLengthPos {
			if ss.longLengthType == lltLiteralLength {
				want.litLength += 0x10000
			} else {
				want.matchLength += 0x10000
			}
		}
		want.offset = testRepOffset(rep, sd.offBase, sd.litLength == 0)
		got := decodeSequence(&st, false, n == nbSeq-1)
		if got != want {
			t.Fatalf("block %d seq %d: got %+v, want %+v", i, n, got, want)
		}
	}
	if !st.dStream.EndOfDStream() {
		t.Fatalf("block %d: sequence bitstream not fully consumed", i)
	}
	// C: ZSTD_decompressSequences_body — save reps for next block.
	for j := range st.prevOffset {
		d.entropy.rep[j] = uint32(st.prevOffset[j])
	}
}

// TestDecodeSeqsLongBits decodes sequences that each read the most extra
// bits (16 literal length, 16 match length and 31 offset bits: 63 in all),
// which needs decodeSequence's mid-sequence reload.
func TestDecodeSeqsLongBits(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const nbSeq = 5
	var vals [nbSeq][3]uint64 // ll, ml, of extra bits
	for i := range vals {
		vals[i] = [3]uint64{rng.Uint64N(1 << 16), rng.Uint64N(1 << 16), rng.Uint64N(1 << 31)}
	}
	// The decoder reads the stream backwards: per sequence offset, match
	// length, then literal length bits (RLE tables: no state bits).
	var c bitstream.CStream
	buf := make([]byte, 64)
	if err := c.Init(buf); err != nil {
		t.Fatal(err)
	}
	for i := nbSeq - 1; i >= 0; i-- {
		c.AddBits(vals[i][0], 16)
		c.FlushBits()
		c.AddBits(vals[i][1], 16)
		c.FlushBits()
		c.AddBits(vals[i][2], 31)
		c.FlushBits()
	}
	n := c.Close()
	// nbSeq, then LL, OF and ML RLE with symbols MaxLL, MaxOff and MaxML.
	section := append([]byte{nbSeq, 0x54, maxLL, maxOff, maxML}, buf[:n]...)

	d := newTestDctx(blockSizeMax)
	got, hSize, e := decodeSeqHeaders(d, section)
	if e != 0 || got != nbSeq || hSize != 5 {
		t.Fatalf("decodeSeqHeaders: nbSeq %d size %d err %d", got, hSize, e)
	}
	var st seqState
	if _, err := st.dStream.Init(section[hSize:]); err != nil {
		t.Fatal(err)
	}
	initFseState(&st.stateLL, &st.dStream, d.llTptr)
	initFseState(&st.stateOffb, &st.dStream, d.ofTptr)
	initFseState(&st.stateML, &st.dStream, d.mlTptr)
	for i := 0; i < nbSeq; i++ {
		sq := decodeSequence(&st, false, i == nbSeq-1)
		if sq.litLength != 0x10000+int(vals[i][0]) || sq.matchLength != 0x10003+int(vals[i][1]) ||
			uint32(sq.offset) != 0x7FFFFFFD+uint32(vals[i][2]) {
			t.Fatalf("seq %d: got %+v, want bits %x", i, sq, vals[i])
		}
	}
	if !st.dStream.EndOfDStream() {
		t.Fatal("bitstream not fully consumed")
	}
}

// TestDecodeSeqsZeroRepeatOffset: a repeat offset that resolves to 0
// (rep0 - 1 with rep0 == 1) becomes C's (size_t)-1, so that the sequence
// executor rejects it. C: ZSTD_decodeSequence, temp -= !temp.
func TestDecodeSeqsZeroRepeatOffset(t *testing.T) {
	var c bitstream.CStream
	buf := make([]byte, 16)
	if err := c.Init(buf); err != nil {
		t.Fatal(err)
	}
	c.AddBits(1, 1) // OF code 1's extra bit
	c.FlushBits()
	n := c.Close()
	// One sequence; LL, OF and ML RLE with symbols 0 (ll0), 1 and 0.
	section := append([]byte{1, 0x54, 0, 1, 0}, buf[:n]...)
	d := newTestDctx(blockSizeMax)
	nbSeq, hSize, e := decodeSeqHeaders(d, section)
	if e != 0 || nbSeq != 1 {
		t.Fatalf("decodeSeqHeaders: nbSeq %d err %d", nbSeq, e)
	}
	st := seqState{prevOffset: [repNum]int{1, 4, 8}}
	if _, err := st.dStream.Init(section[hSize:]); err != nil {
		t.Fatal(err)
	}
	initFseState(&st.stateLL, &st.dStream, d.llTptr)
	initFseState(&st.stateOffb, &st.dStream, d.ofTptr)
	initFseState(&st.stateML, &st.dStream, d.mlTptr)
	sq := decodeSequence(&st, false, true)
	if uint(sq.offset) != ^uint(0) || st.prevOffset != [repNum]int{sq.offset, 1, 4} {
		t.Fatalf("offset %d, prevOffset %v; want -1 and [-1 1 4]", sq.offset, st.prevOffset)
	}
	if sq.litLength != 0 || sq.matchLength != 3 || !st.dStream.EndOfDStream() {
		t.Fatalf("seq %+v, end %v", sq, st.dStream.EndOfDStream())
	}
}

// checkTableBound checks every cell of a sequence table (header first)
// against nextState + (1<<nbBits) <= 1<<tableLog: the next state is always
// inside the table.
func checkTableBound(t testing.TB, what string, dt []seqSymbol) {
	t.Helper()
	tableLog := dt[0].baseValue
	size := uint32(1) << tableLog
	for u, c := range dt[1 : 1+size] {
		if c.nbBits > 31 || uint32(c.nextState)+1<<c.nbBits > size {
			t.Fatalf("%s: cell %d of tableLog %d: nextState %d nbBits %d", what, u, tableLog, c.nextState, c.nbBits)
		}
	}
}

// TestBuildFSETableBound builds tables from random valid normalized
// counts, with and without low-probability (-1) symbols, and checks the
// next-state bound of every cell.
func TestBuildFSETableBound(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	var wksp buildFSETableWksp
	var dt [1 + 1<<maxFSELog]seqSymbol
	for iter := 0; iter < 20000; iter++ {
		maxSV := rng.IntN(maxSeq + 1)
		tableLog := 5 + rng.IntN(maxFSELog-5+1)
		size := 1 << tableLog
		norm := make([]int16, maxSV+1)
		lowProb := iter%2 == 0
		remaining := size
		var positive []int
		for s := range norm {
			r := rng.IntN(10)
			switch {
			case r < 3 || remaining == 0:
			case lowProb && r < 5:
				norm[s] = -1
				remaining--
			default:
				norm[s] = 1
				remaining--
				positive = append(positive, s)
			}
		}
		if remaining > 0 && len(positive) == 0 {
			if norm[0] == 0 {
				remaining--
			}
			norm[0] = 1
			positive = append(positive, 0)
		}
		for remaining > 0 {
			s := positive[rng.IntN(len(positive))]
			add := 1 + rng.IntN(remaining)
			norm[s] += int16(add)
			remaining -= add
		}
		sum := 0
		for _, c := range norm {
			if c == -1 {
				sum++
			} else {
				sum += int(c)
			}
		}
		if sum != size {
			t.Fatalf("generator: sum %d, want %d", sum, size)
		}
		buildFSETable(dt[:], norm, uint(maxSV), mlBase[:], mlBits[:], uint(tableLog), &wksp)
		if dt[0].baseValue != uint32(tableLog) {
			t.Fatalf("header tableLog %d, want %d", dt[0].baseValue, tableLog)
		}
		checkTableBound(t, fmt.Sprint("norm ", norm), dt[:])
	}
	for _, tc := range []struct {
		name string
		dt   []seqSymbol
	}{{"LL", llDefaultDTable[:]}, {"OF", ofDefaultDTable[:]}, {"ML", mlDefaultDTable[:]}} {
		checkTableBound(t, tc.name+" default", tc.dt)
	}
}

// TestBuildFSETableDefaults rebuilds C's default tables from the default
// distributions, as C's comment says they were generated.
func TestBuildFSETableDefaults(t *testing.T) {
	var wksp buildFSETableWksp
	for _, tc := range []struct {
		name     string
		norm     []int16
		tableLog uint
		base     []uint32
		bits     []uint8
		want     []seqSymbol
	}{
		{"LL", llDefaultNorm[:], llDefaultNormLog, llBase[:], llBits[:], llDefaultDTable[:]},
		{"OF", ofDefaultNorm[:], ofDefaultNormLog, ofBase[:], ofBits[:], ofDefaultDTable[:]},
		{"ML", mlDefaultNorm[:], mlDefaultNormLog, mlBase[:], mlBits[:], mlDefaultDTable[:]},
	} {
		dt := make([]seqSymbol, len(tc.want))
		buildFSETable(dt, tc.norm, uint(len(tc.norm)-1), tc.base, tc.bits, tc.tableLog, &wksp)
		if dt[0].baseValue != tc.want[0].baseValue {
			t.Errorf("%s: tableLog %d, want %d", tc.name, dt[0].baseValue, tc.want[0].baseValue)
		}
		for u := 1; u < len(dt); u++ {
			if dt[u] != tc.want[u] {
				t.Errorf("%s: cell %d = %+v, want %+v", tc.name, u-1, dt[u], tc.want[u])
			}
		}
	}
}

// litSection builds a literals section of type typ for lits. For
// setCompressed it builds a new Huffman table into ct; for setRepeat it
// reuses ct.
func litSection(t *testing.T, lits []byte, typ symbolEncodingType, ct *hufCTables) []byte {
	t.Helper()
	dst := make([]byte, len(lits)+1024)
	switch typ {
	case setBasic:
		n, err := noCompressLiterals(dst, lits)
		if err != nil {
			t.Fatal(err)
		}
		return dst[:n]
	case setRLE:
		return dst[:compressRleLiteralsBlock(dst, lits)]
	}
	srcSize := len(lits)
	lhSize := 3 + b2i(srcSize >= 1<<10) + b2i(srcSize >= 16<<10)
	singleStream := srcSize < 256
	repeat, flags := huf.RepeatNone, 0
	if typ == setRepeat {
		repeat, flags = huf.RepeatValid, huf.FlagPreferRepeat
	}
	hufCompress := huf.Compress4XRepeat
	if singleStream {
		hufCompress = huf.Compress1XRepeat
	}
	wksp := make([]byte, huf.WorkspaceSize)
	cLitSize, err := hufCompress(dst[lhSize:], lits, huf.SymbolValueMax, litHufLog, wksp, ct.cTable[:], &repeat, flags)
	if err != nil || cLitSize < 2 {
		t.Fatalf("huf compress: %d %v", cLitSize, err)
	}
	if (typ == setRepeat) != (repeat != huf.RepeatNone) {
		t.Fatalf("huf compress: repeat %v for type %d", repeat, typ)
	}
	switch lhSize {
	case 3:
		lhc := uint32(typ) + uint32(b2i(!singleStream))<<2 + uint32(srcSize)<<4 + uint32(cLitSize)<<14
		dst[0], dst[1], dst[2] = byte(lhc), byte(lhc>>8), byte(lhc>>16)
	case 4:
		binary.LittleEndian.PutUint32(dst, uint32(typ)+2<<2+uint32(srcSize)<<4+uint32(cLitSize)<<18)
	case 5:
		binary.LittleEndian.PutUint32(dst, uint32(typ)+3<<2+uint32(srcSize)<<4+uint32(cLitSize)<<22)
		dst[4] = byte(cLitSize >> 10)
	}
	return dst[:lhSize+cLitSize]
}

// TestLitBufferPlacements decodes the same literals with every literals
// type into each of the three buffer placements (in_dst, not_in_dst and
// split) and checks the placement and the decoded literals.
func TestLitBufferPlacements(t *testing.T) {
	const bsMax = blockSizeMax
	for _, size := range []int{200, 3000, 60000, litBufferExtraSize, litBufferExtraSize + 1, 100000, bsMax} {
		text := creftest.Inputs(t)["zstd-format.md"]
		lits := bytes.Repeat(text, size/len(text)+1)[:size]
		rle := bytes.Repeat([]byte{'z'}, size)
		var ct hufCTables
		compressed := litSection(t, lits, setCompressed, &ct)
		repeated := litSection(t, lits, setRepeat, &ct)
		cases := []struct {
			name    string
			section []byte
			want    []byte
		}{
			{"basic", litSection(t, lits, setBasic, nil), lits},
			{"rle", litSection(t, rle, setRLE, nil), rle},
			{"compressed", compressed, lits},
			{"repeat", repeated, lits},
		}
		big := size > litBufferExtraSize
		placements := []struct {
			name        string
			streaming   streamingOperation
			dstCapacity int
			want        litLocation
		}{
			{"in_dst", notStreaming, bsMax + 2*wildcopyOverlength + size + 1, inDst},
			{"in_dst-boundary", notStreaming, bsMax + 2*wildcopyOverlength + size, map[bool]litLocation{false: notInDst, true: split}[big]},
			{"not_in_dst-or-split", isStreaming, bsMax, map[bool]litLocation{false: notInDst, true: split}[big]},
			{"tight", notStreaming, size, map[bool]litLocation{false: notInDst, true: split}[big]},
		}
		for _, c := range cases {
			for _, p := range placements {
				t.Run(fmt.Sprintf("%d/%s/%s", size, c.name, p.name), func(t *testing.T) {
					d := newTestDctx(bsMax)
					if c.name == "repeat" { // load the table first, as C's previous block would
						if _, e := d.decodeLiteralsBlock(compressed, make([]byte, bsMax), bsMax, notStreaming); e != 0 {
							t.Fatalf("table block: %d", e)
						}
					}
					dst := make([]byte, p.dstCapacity)
					n, e := d.decodeLiteralsBlock(c.section, dst, p.dstCapacity, p.streaming)
					if e != 0 {
						t.Fatalf("decodeLiteralsBlock: %d", e)
					}
					if n != len(c.section) {
						t.Fatalf("read %d bytes, section has %d", n, len(c.section))
					}
					if d.litBufferLocation != p.want {
						t.Fatalf("location %d, want %d", d.litBufferLocation, p.want)
					}
					if got := decodedLits(d); !bytes.Equal(got, c.want) {
						t.Fatalf("literals differ (%d bytes, want %d)", len(got), len(c.want))
					}
					checkLitOffsets(t, d, dst, p.dstCapacity, size)
				})
			}
		}
		// Raw literals followed by at least WILDCOPY_OVERLENGTH bytes are
		// referenced in place.
		t.Run(fmt.Sprintf("%d/basic/in_src", size), func(t *testing.T) {
			d := newTestDctx(bsMax)
			block := append(litSection(t, lits, setBasic, nil), make([]byte, wildcopyOverlength)...)
			if _, e := d.decodeLiteralsBlock(block, make([]byte, bsMax), bsMax, isStreaming); e != 0 {
				t.Fatalf("decodeLiteralsBlock: %d", e)
			}
			if d.litBufferLocation != notInDst || &d.litBuf[0] != &block[0] || !bytes.Equal(decodedLits(d), lits) {
				t.Fatalf("raw literals not referenced in place")
			}
		})
	}
}

// checkLitOffsets checks d's literal buffer offsets against C's
// ZSTD_allocateLiteralsBuffer formulas (for a Huffman split, after
// ZSTD_decodeLiteralsBlock's fix-up, which gives the same offsets as the
// immediate split). dst is the dst decodeLiteralsBlock was given.
func checkLitOffsets(t *testing.T, d *dctx, dst []byte, dstCapacity, litSize int) {
	t.Helper()
	bsMax := d.blockSizeMax()
	ews := min(bsMax, dstCapacity) // C: expectedWriteSize
	var buf, end int
	inExtra := false
	switch d.litBufferLocation {
	case inDst:
		buf = bsMax + wildcopyOverlength
		end = buf + litSize
	case notInDst:
		buf, end, inExtra = 0, litSize, true
	case split:
		buf = ews - litSize + litBufferExtraSize - wildcopyOverlength
		end = ews - wildcopyOverlength
	}
	if d.litBuffer != buf || d.litBufferEnd != end || d.litPtr != buf {
		t.Fatalf("location %d: litBuffer %d litBufferEnd %d litPtr %d, want %d %d %d",
			d.litBufferLocation, d.litBuffer, d.litBufferEnd, d.litPtr, buf, end, buf)
	}
	want := &dst[0]
	if inExtra {
		want = &d.litExtraBuffer[0]
	}
	if &d.litBuf[0] != want {
		t.Fatalf("location %d: litBuf is not the expected array", d.litBufferLocation)
	}
}

// TestLitBufferDstTooSmall: literals that fit blockSizeMax but not
// dstCapacity are dstSize_tooSmall (C: expectedWriteSize < litSize).
func TestLitBufferDstTooSmall(t *testing.T) {
	const size = 1000
	text := creftest.Inputs(t)["zstd-format.md"][:size]
	var ct hufCTables
	for _, section := range [][]byte{
		litSection(t, text, setBasic, nil),
		litSection(t, bytes.Repeat([]byte{'z'}, size), setRLE, nil),
		litSection(t, text, setCompressed, &ct),
	} {
		for _, dstCapacity := range []int{size - 1, size} {
			d := newTestDctx(blockSizeMax)
			_, e := d.decodeLiteralsBlock(section, make([]byte, dstCapacity), dstCapacity, notStreaming)
			want := dErr(0)
			if dstCapacity < size {
				want = dErrLitDstSizeTooSmall
			}
			if e != want {
				t.Errorf("type %d, dstCapacity %d: err %d, want %d", section[0]&3, dstCapacity, e, want)
			}
		}
	}
}

// TestLitBufferTreelessWithoutTable: a treeless (set_repeat) literals
// section with no Huffman table loaded before must fail as C's does
// (dictionary_corrupted from the litEntropy check), not decode with an
// empty DTable (which makes the X2 loops spin forever).
func TestLitBufferTreelessWithoutTable(t *testing.T) {
	text := creftest.Inputs(t)["zstd-format.md"]
	for _, size := range []int{100, 5000} {
		var ct hufCTables
		litSection(t, text[:size], setCompressed, &ct)
		section := litSection(t, text[:size], setRepeat, &ct)
		d := newTestDctx(blockSizeMax)
		_, e := d.decodeLiteralsBlock(section, make([]byte, blockSizeMax), blockSizeMax, notStreaming)
		if e != dErrDictionaryCorrupted {
			t.Fatalf("size %d: err %d, want dictionary_corrupted", size, e)
		}
	}
}

// crefOneBlockFrame wraps block as the only (last, compressed) block of a
// single-segment frame of content size fcs.
func crefOneBlockFrame(block []byte, fcs uint32) []byte {
	f := binary.LittleEndian.AppendUint32(nil, magicNumber)
	f = append(f, 0xA0) // FCS field 4 bytes, single segment
	f = binary.LittleEndian.AppendUint32(f, fcs)
	bh := uint32(len(block))<<3 | btCompressed<<1 | 1
	f = append(f, byte(bh), byte(bh>>8), byte(bh>>16))
	return append(f, block...)
}

// TestDecodeSeqsHeaderErrorsCref runs hostile literals and sequences
// headers through C (each as the only block of a frame). Each hand-made case
// has its exact expected Go result: an error must also be C's error code,
// and 0 means both headers decode (C may fail later, in code not ported
// here). For random mutations of real blocks it checks one direction:
// whenever Go fails, C fails with the same code, and Go accepts every block
// C decodes.
func TestDecodeSeqsHeaderErrorsCref(t *testing.T) {
	type tc struct {
		block []byte
		fcs   uint32
		hand  bool // want is the exact expected result
		want  dErr
	}
	var cases []tc
	add := func(want dErr, fcs uint32, b ...byte) { cases = append(cases, tc{b, fcs, true, want}) }
	// Literals headers.
	add(dErrBlockTooSmall, 100, 0x00)                                                // srcSize < MIN_CBLOCK_SIZE
	add(dErrBlockTooSmall, 100, 0x02, 0x00, 0x00, 0x00)                              // compressed, srcSize < 5
	add(dErrBlockTooSmall, 100, 0x0e, 0x00, 0x00, 0x00)                              // compressed lhlCode 3, srcSize 4 < 5
	add(dErrDictionaryCorrupted, 100, 0x03, 0x00, 0x00, 0x00, 0x00)                  // treeless, no table
	add(dErrDictionaryCorrupted, 100, 0x0f, 0x00, 0x00, 0x00, 0x00)                  // treeless, no table, lhlCode 3
	add(dErrLiteralsHeaderWrong, 100, 0x56, 0x00, 0x00, 0x00, 0x00)                  // 4 streams, litSize 5
	add(dErrCorruptionDetected, 100, 0x66, 0x80, 0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0) // 4 streams, litSize 6: header ok, HUF fails
	add(dErrLitSizeTooLarge, 100, 0x02, 0xf0, 0x07, 0x00, 0x00)                      // litSize 768 > blockSizeMax 100
	add(dErrLitSizeTooLarge, 100, 0x52, 0x46, 0x00, 0x00, 0x00)                      // compressed litSize 101 > blockSizeMax 100
	add(dErrCorruptionDetected, 100, 0x42, 0x46, 0x00, 0x00, 0x00)                   // compressed litSize 100: header ok, HUF fails
	add(dErrLiteralsHeaderWrong, 4000, 0x0e, 0x00, 0x00, 0x40, 0x00, 0x00)           // lhlCode 3, 4 streams, litSize 0
	add(dErrBlockTooSmall, 4000, 0x0e, 0x40, 0x00, 0x40, 0x00, 0x00)                 // lhlCode 3, litSize 1024, litCSize 256 past src
	add(dErrLitSizeTooLarge, 4000, 0x0a, 0x00, 0x01, 0x00, 0x01, 0x00)               // lhlCode 2, litSize 4096 > 4000
	add(dErrCorruptionDetected, 4000, 0x0a, 0x40, 0x00, 0x00, 0x01, 0x00)            // lhlCode 2, litSize 1024, litCSize 0: HUF fails
	add(dErrBlockTooSmall, 100, 0x0c, 0x00)                                          // raw, lhlCode 3, srcSize < 3
	add(dErrBlockTooSmall, 100, 0x04, 0x05)                                          // raw lhlCode 1, litSize 80 past src
	add(dErrLitSizeTooLarge, 40, 0x04, 0x05, 0x00)                                   // raw litSize 80 > blockSizeMax 40
	add(dErrBlockTooSmall, 100, 0x05, 0x00)                                          // RLE lhlCode 1, srcSize < 3
	add(dErrBlockTooSmall, 100, 0x0d, 0x00, 0x00)                                    // RLE lhlCode 3, srcSize < 4
	add(dErrLitSizeTooLarge, 10, 0xf9, 'a', 0x00)                                    // RLE litSize 31 > blockSizeMax 10
	add(dErrLitSizeTooLarge, 100, 0x55, 0x06, 'a')                                   // RLE litSize 101 > blockSizeMax 100
	add(dErrSeqHeaderTooSmall, 100, 0x45, 0x06, 'a')                                 // RLE litSize 100, no sequences section
	add(dErrSeqHeaderTooSmall, 100, 0x09, 'a')                                       // RLE 1 literal, no sequences section
	// Sequences headers after 1 RLE literal (0x09 'a').
	seqs := func(want dErr, fcs uint32, b ...byte) { add(want, fcs, append([]byte{0x09, 'a'}, b...)...) }
	seqs(0, 100, 0x00)                                              // nbSeq 0
	seqs(dErrCorruptionDetected, 100, 0x00, 0x00)                   // nbSeq 0 with extra data
	seqs(dErrSeqHeaderTooSmall, 100, 0x80)                          // 2-byte nbSeq cut
	seqs(dErrSeqHeaderTooSmall, 100, 0xff, 0x00)                    // 3-byte nbSeq cut
	seqs(dErrSeqHeaderTooSmall, 100, 0x01)                          // no table descriptor byte
	seqs(dErrCorruptionDetected, 100, 0x01, 0x01)                   // reserved bit 0 set
	seqs(dErrCorruptionDetected, 100, 0x01, 0x02)                   // reserved bit 1 set
	seqs(dErrCorruptionDetected, 100, 0x01, 0xfc)                   // all repeat, no fseEntropy
	seqs(dErrBlockTooSmall, 100, 0x01, 0x40)                        // LL RLE, symbol missing
	seqs(dErrCorruptionDetected, 100, 0x01, 0x40, 0x24)             // LL RLE symbol 36 > MaxLL
	seqs(dErrCorruptionDetected, 100, 0x01, 0x10, 0x20)             // OF RLE symbol 32 > MaxOff
	seqs(dErrCorruptionDetected, 100, 0x01, 0x04, 0x35)             // ML RLE symbol 53 > MaxML
	seqs(dErrCorruptionDetected, 100, 0x01, 0x80)                   // LL FSE header missing
	seqs(dErrCorruptionDetected, 100, 0x01, 0x80, 0x0f, 0x00, 0x00) // LL FSE tableLog 20
	seqs(dErrCorruptionDetected, 100, 0x01, 0x80, 0x05, 0x00, 0x00) // LL FSE tableLog 10 > 9
	seqs(dErrCorruptionDetected, 100, 0x01, 0x20, 0x04, 0x00)       // OF FSE tableLog 9 > 8
	seqs(0, 100, 0x01, 0x00, 0x08)                                  // predefined, bitstream 0x08

	// Mutations of real first blocks.
	rng := rand.New(rand.NewPCG(3, 5))
	inputs := creftest.Inputs(t)
	for _, name := range []string{"zstd-format.md", "text16KiB", "period5", "litrle", "nbseqlong", "random-small"} {
		frame := creftest.Run(t, inputs[name], "compress", "-level", "3", "-pledged", "1", "-checksum", "0")
		var zfh frameHeader
		getFrameHeaderAdvanced(&zfh, frame)
		cSize, bp, _ := getcBlockSize(frame[zfh.headerSize:])
		if bp.blockType != btCompressed {
			continue
		}
		block := frame[int(zfh.headerSize)+blockHeaderSize:][:cSize]
		fcs := uint32(min(len(inputs[name]), blockSizeMax))
		cases = append(cases, tc{block: block, fcs: fcs})
		for k := 0; k < 40; k++ {
			m := append([]byte(nil), block...)
			switch k % 4 {
			case 0: // flip a byte in the headers
				m[rng.IntN(min(len(m), 24))] ^= byte(1 + rng.IntN(255))
			case 1: // truncate
				m = m[:rng.IntN(len(m))]
			case 2: // flip anywhere
				m[rng.IntN(len(m))] ^= byte(1 + rng.IntN(255))
			case 3: // smaller frame
				cases = append(cases, tc{block: m, fcs: uint32(rng.IntN(int(fcs) + 1))})
				continue
			}
			cases = append(cases, tc{block: m, fcs: fcs})
		}
	}

	compared := 0
	for i, c := range cases {
		frame := crefOneBlockFrame(c.block, c.fcs)
		var zfh frameHeader
		if need, e := getFrameHeaderAdvanced(&zfh, frame); need != 0 || e != 0 {
			t.Fatalf("case %d: frame header %d %d", i, need, e)
		}
		d := new(dctx)
		d.decompressBegin()
		d.fParams = zfh
		if len(c.block) > d.blockSizeMax() {
			if c.hand {
				t.Fatalf("case %d: block larger than blockSizeMax", i)
			}
			continue // C: srcSize_wrong before the literals
		}
		var dst []byte
		if c.fcs > 0 {
			dst = make([]byte, c.fcs)
		} else {
			dst = make([]byte, 0, 1) // cref's malloc(1): not NULL
		}
		n, e := d.decodeLiteralsBlock(c.block, dst, int(c.fcs), notStreaming)
		if e == 0 {
			_, _, e = decodeSeqHeaders(d, c.block[n:])
		}
		_, cerr := creftest.DecodeCRef(t, frame)
		if c.hand {
			compared++
			if e != c.want {
				t.Errorf("case %d (block %x, fcs %d): Go error %d, want %d", i, c.block, c.fcs, e, c.want)
			}
			if c.want != 0 && (cerr == nil || cerr.Code != c.want.cCode()) {
				t.Errorf("case %d (block %x, fcs %d): C %v, want C code %d", i, c.block, c.fcs, cerr, c.want.cCode())
			}
			continue
		}
		switch {
		case e != 0:
			compared++
			if cerr == nil || cerr.Code != e.cCode() {
				t.Errorf("case %d (block %x, fcs %d): Go error %d (C code %d), C %v", i, c.block[:min(len(c.block), 16)], c.fcs, e, e.cCode(), cerr)
			}
		case cerr == nil:
			compared++
		}
	}
	t.Logf("%d cases, %d compared with C", len(cases), compared)
}

// FuzzDecodeSeqHeaders runs hostile input through decodeSeqHeaders (after
// decodeLiteralsBlock when mode&2) and decodes the sequences, checking no
// panic and that every built table keeps its next states in range.
func FuzzDecodeSeqHeaders(f *testing.F) {
	ents, _ := os.ReadDir(seedsDir)
	for _, e := range ents {
		frame, err := os.ReadFile(filepath.Join(seedsDir, e.Name()))
		if err != nil {
			continue
		}
		var zfh frameHeader
		if need, de := getFrameHeaderAdvanced(&zfh, frame); need != 0 || de != 0 || zfh.frameType != zstdFrame {
			continue
		}
		ip := int(zfh.headerSize)
		for ip < len(frame) {
			cSize, bp, de := getcBlockSize(frame[ip:])
			if de != 0 || ip+blockHeaderSize+cSize > len(frame) {
				break
			}
			if bp.blockType == btCompressed {
				f.Add(frame[ip+blockHeaderSize:ip+blockHeaderSize+cSize], byte(2))
			}
			ip += blockHeaderSize + cSize
			if bp.lastBlock {
				break
			}
		}
	}
	f.Add([]byte{0x01, 0x00, 0x08}, byte(0))
	f.Add([]byte{0x02, 0x54, 0x01, 0x02, 0x03, 0x04}, byte(1))
	f.Fuzz(func(t *testing.T, in []byte, mode byte) {
		d := newTestDctx(blockSizeMax)
		d.fseEntropy = mode&1 != 0
		if mode&2 != 0 {
			n, e := d.decodeLiteralsBlock(in, make([]byte, blockSizeMax), blockSizeMax, streamingOperation(mode>>2&1))
			if e != 0 {
				return
			}
			in = in[n:]
		}
		nbSeq, hSize, e := decodeSeqHeaders(d, in)
		if e != 0 {
			return
		}
		if hSize > len(in) {
			t.Fatalf("header size %d > input %d", hSize, len(in))
		}
		for _, dt := range [][]seqSymbol{d.llTptr, d.ofTptr, d.mlTptr} {
			if dt[0].baseValue > maxFSELog {
				t.Fatalf("tableLog %d", dt[0].baseValue)
			}
			checkTableBound(t, "table", dt)
		}
		if nbSeq == 0 {
			return
		}
		var st seqState
		for j := range st.prevOffset {
			st.prevOffset[j] = int(d.entropy.rep[j])
		}
		if _, err := st.dStream.Init(in[hSize:]); err != nil {
			return
		}
		initFseState(&st.stateLL, &st.dStream, d.llTptr)
		initFseState(&st.stateOffb, &st.dStream, d.ofTptr)
		initFseState(&st.stateML, &st.dStream, d.mlTptr)
		for n := 0; n < nbSeq; n++ {
			decodeSequence(&st, false, n == nbSeq-1)
		}
	})
}

// TestDecodeSeqsErrorMapping pins the sentinels and C codes of the error
// codes the literals and sequences headers add (the mapping table rows
// ErrBlockTooSmall and ErrUnexpectedBlockSize).
func TestDecodeSeqsErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		e     dErr
		cCode int
		want  error
	}{
		{dErrBlockTooSmall, 20, ErrBlockTooSmall},
		{dErrSeqHeaderTooSmall, 72, ErrBlockTooSmall},
		{dErrLitSizeTooLarge, 20, ErrUnexpectedBlockSize},
		{dErrLitDstSizeTooSmall, 70, ErrUnexpectedBlockSize},
	} {
		if got := tc.e.cCode(); got != tc.cCode {
			t.Errorf("dErr %d: C code %d, want %d", tc.e, got, tc.cCode)
		}
		if got := tc.e.toError("block"); got != tc.want {
			t.Errorf("dErr %d: %v, want %v", tc.e, got, tc.want)
		}
	}
	for _, e := range []dErr{dErrLiteralsHeaderWrong, dErrDictionaryCorrupted} {
		if _, ok := e.toError("literals").(*corruptError); !ok {
			t.Errorf("dErr %d is not a corruptError", e)
		}
	}
}

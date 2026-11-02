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
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
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
	add(dErrLitSizeTooLarge, 100, 0x54, 0x06, 0x00)                                  // raw litSize 101 > blockSizeMax 100
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
// codes the block decoder adds (the mapping table rows ErrBlockTooSmall,
// ErrUnexpectedBlockSize, ErrWindowSizeTooSmall and
// ErrCompressedSizeTooBig).
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
		{dErrBlockDstSizeTooSmall, 70, ErrUnexpectedBlockSize},
		{dErrOffsetBeyondHistory, 20, ErrWindowSizeTooSmall},
		{dErrCompressedSizeTooBig, 72, ErrCompressedSizeTooBig},
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

// blockMode is how decodeFrameByBlocks calls the block decoder.
type blockMode int

const (
	// bmBlock: decompressBlock (outside a frame, blockSizeMax 128 KiB), the
	// output space the rest of the content; history checked per block.
	bmBlock blockMode = iota
	// bmFrame: C's ZSTD_decompressFrame: the frame's blockSizeMax, not
	// streaming, the output space the rest of the content, checkContinuity
	// once.
	bmFrame
	// bmStream: the frame's blockSizeMax, streaming, the output space
	// min(rest, blockSizeMax), checkContinuity per block (as
	// ZSTD_decompressContinue does).
	bmStream
)

// decodeFrameByBlocks decodes the single frame in frame block by block,
// test-locally, with no checksum check. The output is total bytes (C's
// dstCapacity), written after pad bytes of garbage. From block index
// segAt on (if >= 0), the output goes to a second buffer, so that the first
// one becomes the extDict. Raw and RLE blocks are copied here, as C's
// ZSTD_copyRawBlock and ZSTD_setRleBlock do; in bmFrame mode the output
// must equal the frame content size, as ZSTD_decompressFrame checks. cold
// sets ddictIsCold before every compressed block, which forces the
// prefetch decoder.
func decodeFrameByBlocks(d *dctx, frame []byte, total, pad, segAt int, mode blockMode, cold bool) ([]byte, dErr) {
	var zfh frameHeader
	if need, e := getFrameHeaderAdvanced(&zfh, frame); e != 0 || need != 0 {
		return nil, dErrSrcSizeWrong
	}
	d.decompressBegin()
	d.fParams = zfh
	d.isFrameDecompression = mode != bmBlock
	newBuf := func(rest int) []byte {
		b := make([]byte, pad, pad+rest)
		for i := range b {
			b[i] = byte(0xA5 ^ i)
		}
		return b
	}
	buf := newBuf(total)
	var done []byte // output of the first segment
	if mode == bmFrame {
		d.checkContinuity(buf, total)
	}
	insert := func(n int) { // C: ZSTD_insertBlock
		d.checkContinuity(buf, n)
		if n > 0 {
			d.previousDstEnd = len(buf) + n
		}
	}
	ip := int(zfh.headerSize)
	for blk := 0; ; blk++ {
		if blk == segAt {
			done = append(done, buf[pad:]...)
			buf = newBuf(total - len(done))
		}
		cSize, bp, e := getcBlockSize(frame[ip:])
		if e != 0 {
			return nil, e
		}
		ip += blockHeaderSize
		if cSize > len(frame)-ip {
			return nil, dErrSrcSizeWrong
		}
		block := frame[ip : ip+cSize]
		rest := cap(buf) - len(buf)
		var n int
		switch bp.blockType {
		case btRaw:
			if cSize > rest {
				return nil, dErrDstSizeTooSmall
			}
			n = cSize
			copy(buf[len(buf):len(buf)+n], block)
			if mode != bmFrame {
				insert(n)
			}
		case btRLE:
			n = int(bp.origSize)
			if n > rest {
				return nil, dErrDstSizeTooSmall
			}
			memset(buf[len(buf):len(buf)+n], block[0])
			if mode != bmFrame {
				insert(n)
			}
		default:
			d.ddictIsCold = cold
			switch mode {
			case bmBlock:
				n, e = d.decompressBlock(buf[:len(buf):cap(buf)], block)
			case bmFrame:
				n, e = d.decompressBlockInternal(buf, rest, block, notStreaming)
			case bmStream:
				dstCap := min(rest, d.blockSizeMax())
				d.checkContinuity(buf, dstCap)
				n, e = d.decompressBlockInternal(buf, dstCap, block, isStreaming)
				if e == 0 && n > 0 {
					d.previousDstEnd = len(buf) + n
				}
			}
			if e != 0 {
				return nil, e
			}
		}
		buf = buf[:len(buf)+n]
		ip += cSize
		if bp.lastBlock {
			break
		}
	}
	out := append(done, buf[pad:]...)
	if mode == bmFrame && zfh.frameContentSize != contentSizeUnknown && uint64(len(out)) != zfh.frameContentSize {
		return out, dErrCorruptionDetected
	}
	return out, 0
}

// TestDecompressBlockCref decodes every single-frame corpus entry without
// a dictionary block by block and compares the output with Want, in four
// ways: decompressBlock with the rest of the content as output space
// (mostly in_dst literals), the frame decoder's calls (frame
// blockSizeMax), streaming calls with at most blockSizeMax of output space
// (not_in_dst and split literals), and decompressBlock with the second
// half of the blocks in another buffer (matches into the extDict and
// spanning it); and three of them again with the prefetch decoder forced.
func TestDecompressBlockCref(t *testing.T) {
	long0 := seqLongBodyBlocks.Load()
	nFrames := 0
	for _, name := range creftest.FrameNames(t) {
		if strings.HasPrefix(name, "dict") || strings.HasPrefix(name, "multi/") || strings.HasPrefix(name, "skippable") {
			continue
		}
		f := creftest.LoadFrame(t, name)
		if f.CErr != "" || f.Dict != nil {
			continue
		}
		fsi := findFrameSizeInfo(f.Data)
		if fsi.err != 0 || fsi.compressedSize != len(f.Data) || isSkippableFrame(f.Data) {
			continue // not a single zstd frame
		}
		nFrames++
		nBlocks := fsi.nbBlocks
		for _, m := range []struct {
			name  string
			mode  blockMode
			pad   int
			segAt int
			cold  bool
		}{
			{"block", bmBlock, 0, -1, false},
			{"frame", bmFrame, 3, -1, false},
			{"stream", bmStream, 7, -1, false},
			{"extdict", bmBlock, 5, max(1, nBlocks/2), false},
			{"frame-prefetch", bmFrame, 0, -1, true},
			{"stream-prefetch", bmStream, 1, -1, true},
			{"extdict-prefetch", bmBlock, 2, max(1, nBlocks/2), true},
		} {
			d := new(dctx)
			got, e := decodeFrameByBlocks(d, f.Data, len(f.Want), m.pad, m.segAt, m.mode, m.cold)
			if e != 0 {
				t.Errorf("%s (%s): error %d", name, m.name, e)
				continue
			}
			if !bytes.Equal(got, f.Want) {
				t.Errorf("%s (%s): output differs (got %d, want %d bytes)", name, m.name, len(got), len(f.Want))
			}
		}
	}
	if nFrames < 100 {
		t.Fatalf("only %d frames", nFrames)
	}
	t.Logf("%d frames, %d blocks took the prefetch decoder", nFrames, seqLongBodyBlocks.Load()-long0)
}

// execFixture is the state around one sequence: history (an extDict and a
// prefix that starts at base in out), output space dstSpace from op, and
// litAvail literals from litPtr 0 (with WILDCOPY_OVERLENGTH bytes after,
// as every placement leaves).
type execFixture struct {
	extDict     []byte
	out         []byte
	prefixStart int
	op, oend    int
	lits        []byte
	litLimit    int
	history     []byte // extDict followed by the prefix
	before      []byte // out before op
}

func newExecFixture(rng *rand.Rand, ext, prefix, dstSpace, litAvail int) *execFixture {
	const base = 3
	f := &execFixture{prefixStart: base, op: base + prefix, oend: base + prefix + dstSpace, litLimit: litAvail}
	if ext > 0 {
		f.extDict = make([]byte, ext)
		for i := range f.extDict {
			f.extDict[i] = byte(rng.Uint32())
		}
	}
	f.out = make([]byte, f.oend)
	for i := range f.out[:f.op] {
		f.out[i] = byte(rng.Uint32())
	}
	f.lits = make([]byte, litAvail+wildcopyOverlength)
	for i := range f.lits {
		f.lits[i] = byte(rng.Uint32())
	}
	f.history = append(append([]byte(nil), f.extDict...), f.out[base:f.op]...)
	f.before = append([]byte(nil), f.out[:f.op]...)
	return f
}

// want returns the bytes the sequence should produce: its literals, then
// its match copied byte by byte from the history.
func (f *execFixture) want(sq seq) []byte {
	h := append(append([]byte(nil), f.history...), f.lits[:sq.litLength]...)
	for i := 0; i < sq.matchLength; i++ {
		h = append(h, h[len(h)-sq.offset])
	}
	return h[len(f.history):]
}

// TestExecSequenceBoundaries runs hand-built sequences through
// execSequence (fast path and execSequenceEnd), execSequenceEnd directly,
// and execSequenceSplitLitBuffer: offsets at the history size and one
// beyond, into and across the extDict, every overlap distance 1..16, a
// match ending at oend, oend-1 and oend+1 and at the fast path's limit,
// literals ending at the literal limit and one past it, and offsets that
// hold C's huge size_t bits (negative ints; on 386 every offset >= 2^31).
func TestExecSequenceBoundaries(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 8))
	type tc struct {
		name                         string
		ext, prefix, dstSpace, avail int
		sq                           seq
		want                         dErr
	}
	var cases []tc
	add := func(c tc) { cases = append(cases, c) }
	const roomy = 1000
	for _, ext := range []int{0, 50} {
		for _, ll := range []int{0, 5, 20} {
			h := ext + 100
			add(tc{"offset=history", ext, 100, roomy, ll, seq{ll, 20, h + ll}, 0})
			add(tc{"offset=history+1", ext, 100, roomy, ll, seq{ll, 20, h + ll + 1}, dErrOffsetBeyondHistory})
			add(tc{"offset=prefix", ext, 100, roomy, ll, seq{ll, 30, 100 + ll}, 0})
			add(tc{"offset=prefix+1", ext, 100, roomy, ll, seq{ll, 30, 101 + ll}, b2e(ext == 0, dErrOffsetBeyondHistory)})
			// tight: the same through execSequenceEnd
			add(tc{"tight offset=history", ext, 100, ll + 20, ll, seq{ll, 20, h + ll}, 0})
			add(tc{"tight offset=history+1", ext, 100, ll + 20, ll, seq{ll, 20, h + ll + 1}, dErrOffsetBeyondHistory})
		}
		if ext > 0 {
			add(tc{"in extDict", ext, 100, roomy, 3, seq{3, 10, 140}, 0})
			add(tc{"ends at extDict end", ext, 100, roomy, 3, seq{3, 47, 150}, 0})
			add(tc{"spans extDict", ext, 100, roomy, 3, seq{3, 60, 150}, 0})
			add(tc{"spans extDict, overlapping", ext, 4, roomy, 0, seq{0, 70, 10}, 0})
			add(tc{"tight spans extDict", ext, 100, 63, 3, seq{3, 60, 150}, 0})
		}
	}
	for off := 1; off <= 16; off++ {
		for _, ml := range []int{3, 7, 8, 9, 15, 16, 17, 40, 100} {
			add(tc{fmt.Sprintf("overlap %d ml %d", off, ml), 0, 20, roomy, 2, seq{2, ml, off}, 0})
			add(tc{fmt.Sprintf("tight overlap %d ml %d", off, ml), 0, 20, 2 + ml, 2, seq{2, ml, off}, 0})
		}
	}
	for _, ll := range []int{0, 1, 16, 17, 40} {
		add(tc{"match ends at oend", 0, 64, ll + 30, ll, seq{ll, 30, 40}, 0})
		add(tc{"match ends at oend-1", 0, 64, ll + 31, ll, seq{ll, 30, 40}, 0})
		add(tc{"match ends at oend+1", 0, 64, ll + 29, ll, seq{ll, 30, 40}, dErrBlockDstSizeTooSmall})
		add(tc{"fast path limit", 0, 64, ll + 30 + wildcopyOverlength, ll, seq{ll, 30, 40}, 0})
		add(tc{"slow path limit", 0, 64, ll + 30 + wildcopyOverlength - 1, ll, seq{ll, 30, 40}, 0})
		add(tc{"literals end at limit", 0, 64, roomy, ll, seq{ll, 30, 40}, 0})
		if ll > 0 {
			add(tc{"literals one past limit", 0, 64, roomy, ll - 1, seq{ll, 30, 40}, dErrCorruptionDetected})
		}
	}
	for _, off := range []int{-1, -2, -4, int(int32(-1 << 31)), int(int32(-1<<31)) + 1} {
		add(tc{fmt.Sprintf("size_t offset %d", off), 50, 100, roomy, 5, seq{5, 20, off}, dErrOffsetBeyondHistory})
		add(tc{fmt.Sprintf("tight size_t offset %d", off), 50, 100, 25, 5, seq{5, 20, off}, dErrOffsetBeyondHistory})
	}

	type execFn func(f *execFixture, sq seq, litPtr *int) (int, dErr)
	fns := []struct {
		name string
		fn   execFn
	}{
		{"execSequence", func(f *execFixture, sq seq, litPtr *int) (int, dErr) {
			return execSequence(f.out, f.op, f.oend, sq, f.lits, litPtr, f.litLimit, f.prefixStart, f.extDict)
		}},
		{"execSequenceEnd", func(f *execFixture, sq seq, litPtr *int) (int, dErr) {
			return execSequenceEnd(f.out, f.op, f.oend, sq, f.lits, litPtr, f.litLimit, f.prefixStart, f.extDict)
		}},
	}
	for _, c := range cases {
		for _, fn := range fns {
			f := newExecFixture(rng, c.ext, c.prefix, c.dstSpace, c.avail)
			litPtr := 0
			n, e := fn.fn(f, c.sq, &litPtr)
			if e != c.want {
				t.Errorf("%s %s %+v: error %d, want %d", fn.name, c.name, c.sq, e, c.want)
				continue
			}
			if !bytes.Equal(f.out[:f.op], f.before) {
				t.Errorf("%s %s: history changed", fn.name, c.name)
			}
			if e != 0 {
				continue
			}
			if n != c.sq.litLength+c.sq.matchLength || litPtr != c.sq.litLength {
				t.Errorf("%s %s: n %d litPtr %d", fn.name, c.name, n, litPtr)
			}
			if w := f.want(c.sq); !bytes.Equal(f.out[f.op:f.op+n], w) {
				t.Errorf("%s %s %+v: output\n%x\nwant\n%x", fn.name, c.name, c.sq, f.out[f.op:f.op+n], w)
			}
		}
	}

	// execSequenceSplitLitBuffer: the literals sit in out at litAt (ahead
	// of op), as in the split phase; oendW is the callers'
	// litPtr + litLength - WILDCOPY_OVERLENGTH.
	for _, c := range []struct {
		name         string
		ext, gap, ll int // literals start gap bytes after op
		sq           seq
		want         dErr
	}{
		{"far", 0, 200, 10, seq{10, 20, 30}, 0},
		{"ll 40", 0, 200, 40, seq{40, 100, 17}, 0},
		{"near: slow path", 0, 30, 10, seq{10, 20, 30}, 0},
		{"overlap 3, slow", 0, 20, 4, seq{4, 30, 3}, 0},
		{"literals at op", 0, 0, 10, seq{10, 5, 30}, 0},
		{"output catches up", 0, -5, 10, seq{10, 5, 30}, dErrBlockDstSizeTooSmall},
		{"literals end at op", 0, -10, 10, seq{10, 5, 30}, 0},
		{"literals past limit", 0, 200, 10, seq{11, 20, 30}, dErrCorruptionDetected},
		{"offset beyond", 0, 200, 10, seq{10, 20, 1000}, dErrOffsetBeyondHistory},
		{"size_t offset", 0, 200, 10, seq{10, 20, -3}, dErrOffsetBeyondHistory},
		{"in extDict", 50, 200, 10, seq{10, 20, 140}, 0},
		{"spans extDict", 50, 200, 10, seq{10, 60, 150}, 0},
		{"offset=history", 50, 200, 10, seq{10, 60, 160}, 0},
		{"offset=history+1", 50, 200, 10, seq{10, 60, 161}, dErrOffsetBeyondHistory},
		{"slow in extDict", 50, 30, 10, seq{10, 20, 140}, 0},
		{"slow spans extDict", 50, 30, 10, seq{10, 60, 150}, 0},
		{"slow offset=history+1", 50, 30, 10, seq{10, 60, 161}, dErrOffsetBeyondHistory},
	} {
		const prefix = 100
		f := newExecFixture(rng, c.ext, prefix, 500, 0)
		litAt := f.op + c.gap
		if c.gap >= 0 {
			for i := range c.ll {
				f.out[litAt+i] = byte(rng.Uint32())
			}
		}
		lits := append([]byte(nil), f.out[litAt:litAt+c.ll]...)
		f.before = append([]byte(nil), f.out[:f.op]...)
		litPtr := litAt
		n, e := execSequenceSplitLitBuffer(f.out, f.op, f.oend, litAt+c.sq.litLength-wildcopyOverlength, c.sq,
			&litPtr, litAt+c.ll, f.prefixStart, f.extDict)
		if e != c.want {
			t.Errorf("split %s: error %d, want %d", c.name, e, c.want)
			continue
		}
		if e != 0 {
			continue
		}
		if !bytes.Equal(f.out[:f.op], f.before) || n != c.sq.litLength+c.sq.matchLength || litPtr != litAt+c.ll {
			t.Errorf("split %s: n %d litPtr %d", c.name, n, litPtr)
		}
		f.lits = lits
		if w := f.want(c.sq); !bytes.Equal(f.out[f.op:f.op+n], w) {
			t.Errorf("split %s: output\n%x\nwant\n%x", c.name, f.out[f.op:f.op+n], w)
		}
	}
}

// b2e returns e when b, else 0.
func b2e(b bool, e dErr) dErr {
	if b {
		return e
	}
	return 0
}

// oneSeqBlock returns a compressed block with raw literals lits and one
// sequence, from RLE tables with LL, OF and ML codes llc, ofc and mlc;
// bits are the sequence's extra bits as the decoder reads them (offset,
// match length, literal length: the encoder writes them in reverse), each
// {value, nbBits}. trailing adds bytes after the bitstream's end mark.
func oneSeqBlock(t *testing.T, lits []byte, llc, ofc, mlc byte, bits [][2]uint64, trailing int) []byte {
	t.Helper()
	b := make([]byte, 1024)
	n, err := noCompressLiterals(b, lits)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b[:n], 1, 0x54, llc, ofc, mlc)
	var c bitstream.CStream
	buf := make([]byte, 64)
	if err := c.Init(buf); err != nil {
		t.Fatal(err)
	}
	for i := len(bits) - 1; i >= 0; i-- {
		c.AddBits(bits[i][0], uint(bits[i][1]))
		c.FlushBits()
	}
	m := c.Close()
	if trailing > 0 { // more bytes before the end mark's byte: never consumed
		b = append(b, make([]byte, trailing)...)
	}
	return append(b, buf[:m]...)
}

// TestDecompressBlockErrorsCref decodes hostile blocks as C's one-shot
// frame decoder does (frame blockSizeMax, output space the frame content
// size, which the output must equal) and compares with `cref decompress`
// on the same frame: the same C error code, or both succeed with the same
// output. Hand-made cases have an exact expected Go error; they include
// offsets of 2^31 and more, which on 386 are negative ints and must be
// rejected as C's size_t comparison does. The rest mutate the compressed
// blocks of real frames (content only, so the frame structure holds).
func TestDecompressBlockErrorsCref(t *testing.T) {
	type tc struct {
		name  string
		frame []byte
		hand  bool
		want  dErr
	}
	var cases []tc
	// A frame with a 1 KiB window (blockSizeMax 1024), content size fcs and
	// one compressed block.
	add := func(name string, want dErr, fcs uint32, block []byte) {
		f := binary.LittleEndian.AppendUint32(nil, magicNumber)
		f = append(f, 0x80, 0x00) // FCS field 4 bytes; window log 10
		f = binary.LittleEndian.AppendUint32(f, fcs)
		bh := uint32(len(block))<<3 | btCompressed<<1 | 1
		f = append(f, byte(bh), byte(bh>>8), byte(bh>>16))
		cases = append(cases, tc{name, append(f, block...), true, want})
	}
	addSingle := func(name string, want dErr, fcs uint32, block []byte) { // blockSizeMax fcs
		cases = append(cases, tc{name, crefOneBlockFrame(block, fcs), true, want})
	}
	of31 := func(v uint64) []byte { return oneSeqBlock(t, nil, 0, 31, 0, [][2]uint64{{v, 31}}, 0) }
	add("offset 2^31-3", dErrOffsetBeyondHistory, 3, of31(0))
	add("offset 2^31", dErrOffsetBeyondHistory, 3, of31(3))
	add("offset 2^31+1", dErrOffsetBeyondHistory, 3, of31(4))
	add("offset 2^32-4", dErrOffsetBeyondHistory, 3, of31(0x7FFFFFFF))
	add("offset 2^32-4 after literals", dErrOffsetBeyondHistory, 103,
		oneSeqBlock(t, bytes.Repeat([]byte{'z'}, 100), 0, 31, 0, [][2]uint64{{0x7FFFFFFF, 31}}, 0))
	add("zero repeat offset", dErrOffsetBeyondHistory, 3, oneSeqBlock(t, nil, 0, 1, 0, [][2]uint64{{1, 1}}, 0))
	add("offset = history", 0, 7, oneSeqBlock(t, []byte("abcd"), 4, 2, 0, [][2]uint64{{3, 2}}, 0))
	add("offset = history+1", dErrOffsetBeyondHistory, 7, oneSeqBlock(t, []byte("abcd"), 4, 3, 0, [][2]uint64{{0, 3}}, 0))
	add("sequence past dst", dErrBlockDstSizeTooSmall, 6, oneSeqBlock(t, []byte("abcd"), 4, 2, 0, [][2]uint64{{3, 2}}, 0))
	add("literals past buffer", dErrCorruptionDetected, 100, oneSeqBlock(t, []byte("abcd"), 5, 2, 0, [][2]uint64{{3, 2}}, 0))
	add("bitstream not consumed", dErrCorruptionDetected, 7, oneSeqBlock(t, []byte("abcd"), 4, 2, 0, [][2]uint64{{3, 2}}, 1))
	add("last literals fit", 0, 7, oneSeqBlock(t, []byte("abcd"), 1, 2, 0, [][2]uint64{{0, 2}}, 0))
	add("last literals past dst", dErrBlockDstSizeTooSmall, 6, oneSeqBlock(t, []byte("abcd"), 1, 2, 0, [][2]uint64{{0, 2}}, 0))
	// in_dst literals (output space 3000 > blockSizeMax+64+4): the block's
	// output must stop where they start, at blockSizeMax+32.
	add("output reaches in_dst literals", dErrBlockDstSizeTooSmall, 3000,
		oneSeqBlock(t, []byte("abcd"), 4, 2, 47, [][2]uint64{{3, 2}, {945, uint64(mlBits[47])}}, 0))
	add("no output space", dErrBlockDstSizeTooSmall, 0, oneSeqBlock(t, nil, 0, 1, 0, [][2]uint64{{0, 1}}, 0))
	addSingle("fcs 0", dErrCompressedSizeTooBig, 0, oneSeqBlock(t, nil, 0, 1, 0, [][2]uint64{{0, 1}}, 0))
	big := append([]byte{0x54, 0x06}, bytes.Repeat([]byte{'q'}, 101)...) // raw litSize 101, no sequences section
	addSingle("block > blockSizeMax", dErrCompressedSizeTooBig, 100, big)
	addSingle("block = blockSizeMax", dErrSeqHeaderTooSmall, 98, append([]byte{0x04, 0x06}, bytes.Repeat([]byte{'q'}, 96)...))

	rng := rand.New(rand.NewPCG(9, 9))
	inputs := creftest.Inputs(t)
	for _, name := range []string{"zstd-format.md", "text16KiB", "text256KiB+1", "period5", "litrle", "nbseqlong", "random-small", "step4-100-302"} {
		for _, level := range []string{"1", "19"} {
			frame := creftest.Run(t, inputs[name], "compress", "-level", level, "-pledged", "1", "-checksum", "0")
			var zfh frameHeader
			getFrameHeaderAdvanced(&zfh, frame)
			var blocks [][2]int // compressed block contents
			for ip := int(zfh.headerSize); ; {
				cSize, bp, _ := getcBlockSize(frame[ip:])
				if bp.blockType == btCompressed {
					blocks = append(blocks, [2]int{ip + blockHeaderSize, cSize})
				}
				ip += blockHeaderSize + cSize
				if bp.lastBlock {
					break
				}
			}
			cases = append(cases, tc{name: name + "/L" + level, frame: frame})
			if len(blocks) == 0 {
				continue
			}
			for k := 0; k < 30; k++ {
				m := append([]byte(nil), frame...)
				blk := blocks[rng.IntN(len(blocks))]
				switch k % 3 {
				case 0: // flip a byte in the block's last 16 bytes (sequence bitstream, read backwards)
					m[blk[0]+blk[1]-1-rng.IntN(min(16, blk[1]))] ^= byte(1 + rng.IntN(255))
				case 1: // flip a byte anywhere in the block
					m[blk[0]+rng.IntN(blk[1])] ^= byte(1 + rng.IntN(255))
				case 2: // flip a bit in the block's first 32 bytes
					i := blk[0] + rng.IntN(min(32, blk[1]))
					m[i] ^= 1 << rng.IntN(8)
				}
				cases = append(cases, tc{name: fmt.Sprintf("%s/L%s/mut%d", name, level, k), frame: m})
			}
		}
	}

	failed, both := 0, 0
	for _, c := range cases {
		var zfh frameHeader
		getFrameHeaderAdvanced(&zfh, c.frame)
		d := new(dctx)
		got, e := decodeFrameByBlocks(d, c.frame, int(zfh.frameContentSize), 0, -1, bmFrame, false)
		cout, cerr := creftest.DecodeCRef(t, c.frame)
		if c.hand && e != c.want {
			t.Errorf("%s: Go error %d, want %d", c.name, e, c.want)
		}
		if c.hand { // the prefetch decoder gives the same (one sequence: same order of checks)
			if _, e := decodeFrameByBlocks(new(dctx), c.frame, int(zfh.frameContentSize), 0, -1, bmFrame, true); e != c.want {
				t.Errorf("%s: prefetch decoder: Go error %d, want %d", c.name, e, c.want)
			}
		}
		switch {
		case e != 0 && cerr == nil:
			t.Errorf("%s: Go error %d (C code %d), C succeeds", c.name, e, e.cCode())
		case e != 0:
			failed++
			if cerr.Code != e.cCode() {
				t.Errorf("%s: Go error %d (C code %d), C %v", c.name, e, e.cCode(), cerr)
			}
		case cerr != nil:
			t.Errorf("%s: Go succeeds, C %v", c.name, cerr)
		default:
			both++
			if !bytes.Equal(got, cout) {
				t.Errorf("%s: output differs from C", c.name)
			}
		}
	}
	t.Logf("%d cases: %d fail in both with the same code, %d succeed in both", len(cases), failed, both)
}

// FuzzDecompressBlock decodes hostile blocks with history (an extDict of
// ext bytes and a prefix of hist bytes) and an output space of dstCap
// bytes; flags pick streaming (1), frame mode with a smaller blockSizeMax
// (2, size from flags>>4), the prefetch decoder (4) and decoding the block
// a second time after the first (8, for repeat tables and offsets). It
// checks there is no panic, the size is within the output space, and the
// history is not written.
func FuzzDecompressBlock(f *testing.F) {
	ents, _ := os.ReadDir(seedsDir)
	for i, e := range ents {
		frame, err := os.ReadFile(filepath.Join(seedsDir, e.Name()))
		if err != nil {
			continue
		}
		var zfh frameHeader
		if need, de := getFrameHeaderAdvanced(&zfh, frame); need != 0 || de != 0 || zfh.frameType != zstdFrame {
			continue
		}
		for ip := int(zfh.headerSize); ip < len(frame); {
			cSize, bp, de := getcBlockSize(frame[ip:])
			if de != 0 || ip+blockHeaderSize+cSize > len(frame) {
				break
			}
			if bp.blockType == btCompressed {
				f.Add(frame[ip+blockHeaderSize:ip+blockHeaderSize+cSize], uint16(i*97), uint16(i*13), uint32(blockSizeMax), byte(i))
			}
			ip += blockHeaderSize + cSize
			if bp.lastBlock {
				break
			}
		}
	}
	f.Add([]byte{0x00, 0x01, 0x54, 0x00, 0x1f, 0x00, 0x00, 0x00, 0x00, 0xc0}, uint16(10), uint16(0), uint32(100), byte(0))
	f.Fuzz(func(t *testing.T, block []byte, hist, ext uint16, dstCap uint32, flags byte) {
		d := new(dctx)
		d.decompressBegin()
		d.isFrameDecompression = flags&2 != 0
		d.fParams.blockSizeMax = uint32(min(blockSizeMax, 1<<(10+flags>>4%8)))
		capMax := 2*d.blockSizeMax() + 2*litBufferExtraSize
		dc := int(dstCap) % (capMax + 1)
		streaming := streamingOperation(flags & 1)
		prev := make([]byte, ext)
		for i := range prev {
			prev[i] = byte(i * 7)
		}
		d.prevDst, d.previousDstEnd = prev, len(prev)
		buf := make([]byte, int(hist), int(hist)+2*dc)
		for i := range buf {
			buf[i] = byte(i * 5)
		}
		d.checkContinuity(buf[:0], len(buf)+dc)
		d.previousDstEnd = len(buf)
		histCopy := append([]byte(nil), buf...)
		for round := 0; round < 1+int(flags>>3&1); round++ {
			d.ddictIsCold = flags&4 != 0
			d.checkContinuity(buf, dc)
			n, e := d.decompressBlockInternal(buf, dc, block, streaming)
			if e != 0 {
				return
			}
			if n < 0 || n > dc {
				t.Fatalf("size %d, output space %d", n, dc)
			}
			if !bytes.Equal(buf[:len(histCopy)], histCopy) || !bytes.Equal(d.extDict, prev) {
				t.Fatal("history written")
			}
			buf = buf[:len(buf)+n]
			d.previousDstEnd = len(buf)
			histCopy = append(histCopy[:0], buf...)
		}
	})
}

// TestDecompressBlockContinuity: output into a new buffer makes the previous segment
// (from prefixStart, not the buffer's start) the extDict; output that
// continues where the previous one ended keeps everything.
func TestDecompressBlockContinuity(t *testing.T) {
	d := new(dctx)
	d.decompressBegin()
	a := make([]byte, 5, 100)
	d.checkContinuity(a, 95)
	if d.prefixStart != 5 || d.virtualStart != 5 || len(d.extDict) != 0 {
		t.Fatalf("first: prefixStart %d virtualStart %d extDict %d", d.prefixStart, d.virtualStart, len(d.extDict))
	}
	a = append(a, "0123456789"...)
	d.previousDstEnd = len(a)
	d.checkContinuity(a, 85) // contiguous
	if d.prefixStart != 5 || len(d.extDict) != 0 {
		t.Fatalf("contiguous: prefixStart %d extDict %d", d.prefixStart, len(d.extDict))
	}
	d.checkContinuity(a[:3], 0) // not contiguous, but no output space
	if d.prefixStart != 5 || d.previousDstEnd != 15 {
		t.Fatalf("dstSize 0: prefixStart %d previousDstEnd %d", d.prefixStart, d.previousDstEnd)
	}
	b := make([]byte, 2, 50)
	d.checkContinuity(b, 48)
	if string(d.extDict) != "0123456789" || d.prefixStart != 2 || d.virtualStart != -8 || d.previousDstEnd != 2 {
		t.Fatalf("new buffer: extDict %q prefixStart %d virtualStart %d previousDstEnd %d",
			d.extDict, d.prefixStart, d.virtualStart, d.previousDstEnd)
	}

	// Each half of C's dst == previousDstEnd alone: the same buffer at
	// another position (a rewind), and another buffer at the same position.
	b = append(b, "abcdef"...)
	d.previousDstEnd = len(b)
	d.checkContinuity(b[:5], 40) // same buffer, len 5 != previousDstEnd 8
	if string(d.extDict) != "abcdef" || d.prefixStart != 5 || d.virtualStart != -1 {
		t.Fatalf("rewind: extDict %q prefixStart %d virtualStart %d", d.extDict, d.prefixStart, d.virtualStart)
	}
	b = append(b[:5], "XYZ"...)
	d.previousDstEnd = len(b)
	c := make([]byte, len(b), 50) // another buffer, len == previousDstEnd
	d.checkContinuity(c, 40)
	if string(d.extDict) != "XYZ" || d.prefixStart != 8 || d.virtualStart != 5 {
		t.Fatalf("other buffer: extDict %q prefixStart %d virtualStart %d", d.extDict, d.prefixStart, d.virtualStart)
	}
}

// TestDecompressBlockContinuityGrowingCap: contiguous calls whose slices of
// one array have a growing capacity, then another buffer. The extDict is
// the earlier output, beyond the first slice's capacity (review I1: a stale
// prevDst made this panic).
func TestDecompressBlockContinuityGrowingCap(t *testing.T) {
	d := new(dctx)
	d.decompressBegin()
	arr := make([]byte, 100)
	block := []byte{0x20, 'a', 'b', 'c', 'd', 0x00} // 4 raw literals, no sequences
	for i, dst := range [][]byte{arr[0:0:10], arr[0:4:100], arr[0:8:100]} {
		n, e := d.decompressBlock(dst, block)
		if e != 0 || n != 4 {
			t.Fatalf("block %d: n %d err %d", i, n, e)
		}
	}
	other := make([]byte, 0, 50)
	if n, e := d.decompressBlock(other, block); e != 0 || n != 4 {
		t.Fatalf("other buffer: n %d err %d", n, e)
	}
	if string(d.extDict) != "abcdabcdabcd" {
		t.Fatalf("extDict %q", d.extDict)
	}
}

// rleSeqsBlock returns a compressed block: raw literals lits, then the
// sequences, all from RLE tables with LL, OF and ML codes llc, ofc (> 1,
// so no repeat offsets) and mlc; each sequence gives its {ll, offset, ml}
// values, which must be in the codes' ranges.
func rleSeqsBlock(t *testing.T, lits []byte, llc, ofc, mlc byte, seqs []seq) []byte {
	t.Helper()
	b := make([]byte, len(lits)+16)
	n, err := noCompressLiterals(b, lits)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) > 127 {
		t.Fatal("too many sequences")
	}
	b = append(b[:n], byte(len(seqs)), 0x54, llc, ofc, mlc)
	var bits [][2]uint64 // in the decoder's read order
	for _, sq := range seqs {
		of, ml, ll := uint64(sq.offset)-uint64(ofBase[ofc]), uint64(sq.matchLength)-uint64(mlBase[mlc]), uint64(sq.litLength)-uint64(llBase[llc])
		if of >= 1<<ofBits[ofc] || ml >= 1<<mlBits[mlc] || ll >= 1<<llBits[llc] {
			t.Fatalf("sequence %+v out of the codes' ranges", sq)
		}
		bits = append(bits, [2]uint64{of, uint64(ofBits[ofc])}, [2]uint64{ml, uint64(mlBits[mlc])}, [2]uint64{ll, uint64(llBits[llc])})
	}
	var c bitstream.CStream
	buf := make([]byte, 16*len(seqs)+16)
	if err := c.Init(buf); err != nil {
		t.Fatal(err)
	}
	for i := len(bits) - 1; i >= 0; i-- {
		c.AddBits(bits[i][0], uint(bits[i][1]))
		c.FlushBits()
	}
	return append(b, buf[:c.Close()]...)
}

// refExecSeqs is the output of seqs over lits, byte by byte.
func refExecSeqs(lits []byte, seqs []seq) []byte {
	var out []byte
	for _, sq := range seqs {
		out = append(out, lits[:sq.litLength]...)
		lits = lits[sq.litLength:]
		for range sq.matchLength {
			out = append(out, out[len(out)-sq.offset])
		}
	}
	return append(out, lits...)
}

// TestDecompressBlockSplitLiterals drives the split-literal loops (raw
// literals over 64 KiB, streaming, so ZSTD_split with an immediate split)
// through their loop-level checks, with both the split body and the
// prefetch decoder. The literals in dst are the first P of them and end at
// min(dstCap, blockSizeMax) - 32; the other 64 KiB are in litExtraBuffer.
func TestDecompressBlockSplitLiterals(t *testing.T) {
	// P: the match wildcopy of the first case writes 16 + 32k bytes from
	// op 64, so its last chunk ends 16 bytes past the literals' end (it
	// would overwrite the next literals) only when that end is not itself a
	// chunk boundary, as with P = 2016.
	const P = 2016
	rng := rand.New(rand.NewPCG(10, 10))
	lits := make([]byte, litBufferExtraSize+P)
	for i := range lits {
		lits[i] = byte(rng.Uint32())
	}
	const llc29, llc25, ofc2, ofc5, mlc52 = 29, 25, 2, 5, 52 // LL [1024,2047], [64,127]; OF [1,4], [29,60]; ML [65539,131074]
	litStart := blockSizeMax - wildcopyOverlength - P        // with dstCap >= blockSizeMax
	for _, c := range []struct {
		name          string
		dstCap        int
		llc, ofc, mlc byte
		seqs          []seq
		want          dErr
	}{
		// The match ends 10 bytes before the sequence's literals end: within
		// WILDCOPY_OVERLENGTH, so the slow path, else the wildcopy would
		// overwrite the next literals in dst.
		{"match ends near its literals", litBufferExtraSize + P + litStart - 10, llc25, ofc5, mlc52,
			[]seq{{64, litStart - 10, 40}}, 0},
		// A sequence past oend while the literals are still in dst.
		{"sequence past oend", 70000, llc25, ofc2, mlc52, []seq{{64, 70000 - 63, 1}}, dErrBlockDstSizeTooSmall},
		// The second sequence's literals cross the split point; what is
		// left of the literals in dst does not fit the output space.
		{"leftover past oend", 70000, llc29, ofc2, mlc52, []seq{{1024, 68500, 1}, {2047, 65539, 1}}, dErrBlockDstSizeTooSmall},
		// No sequence crosses the split point; the rest of the literals in
		// dst does not fit the output space.
		{"last segment past oend", 70000, llc29, ofc2, mlc52, []seq{{1024, 68500, 1}}, dErrBlockDstSizeTooSmall},
		// Literals ending exactly at the split point, and one past it.
		{"literals end at split point", litBufferExtraSize + P + 65539, llc29, ofc2, mlc52, []seq{{P, 65539, 1}}, 0},
		{"literals cross split point by 1", litBufferExtraSize + P + 65539, llc29, ofc2, mlc52, []seq{{P + 1, 65539, 1}}, 0},
		{"two sequences, second crosses", litBufferExtraSize + P + 2*65539, llc29, ofc2, mlc52,
			[]seq{{1024, 65539, 3}, {1500, 65539, 2}}, 0},
	} {
		block := rleSeqsBlock(t, lits, c.llc, c.ofc, c.mlc, c.seqs)
		want := refExecSeqs(lits, c.seqs)
		for _, cold := range []bool{false, true} {
			d := new(dctx)
			d.decompressBegin()
			d.isFrameDecompression = false
			buf := make([]byte, 0, c.dstCap)
			d.checkContinuity(buf, c.dstCap)
			d.ddictIsCold = cold
			long0 := seqLongBodyBlocks.Load()
			n, e := d.decompressBlockInternal(buf, c.dstCap, block, isStreaming)
			if took := seqLongBodyBlocks.Load() != long0; took != cold {
				t.Errorf("%s (cold %v): prefetch decoder %v", c.name, cold, took)
			}
			if e != c.want {
				t.Errorf("%s (cold %v): error %d, want %d", c.name, cold, e, c.want)
				continue
			}
			if e == 0 && !bytes.Equal(buf[:n], want) {
				t.Errorf("%s (cold %v): output differs (%d bytes, want %d)", c.name, cold, n, len(want))
			}
		}
	}
}

// TestDecompressBlockOffsetInfo pins getOffsetInfo: the share of cells
// with more than 22 offset bits, scaled to OffFSELog, and the most bits.
func TestDecompressBlockOffsetInfo(t *testing.T) {
	for _, c := range []struct {
		tableLog    uint32
		bits        []uint8 // the cells' nbAdditionalBits, the rest 0
		share, maxB uint32
	}{
		{8, []uint8{23, 23, 23, 23, 23, 23, 23, 22}, 7, 23},
		{8, []uint8{22, 22, 5}, 0, 22},
		{5, []uint8{23, 24}, 16, 24},
		{0, []uint8{30}, 256, 30},
	} {
		dt := make([]seqSymbol, 1+1<<c.tableLog)
		dt[0].baseValue = c.tableLog
		for i, b := range c.bits {
			dt[1+i].nbAdditionalBits = b
		}
		if got := getOffsetInfo(dt, 1); got != (offsetInfo{c.share, c.maxB}) {
			t.Errorf("tableLog %d bits %v: %+v, want share %d max %d", c.tableLog, c.bits, got, c.share, c.maxB)
		}
		if got := getOffsetInfo(dt, 0); got != (offsetInfo{}) {
			t.Errorf("nbSeq 0: %+v", got)
		}
	}
}

// TestDecompressBlockPrefetchChoice pins when decompressBlockInternal
// picks the prefetch decoder: history over 16 MiB, more than 8 sequences,
// and at least 7 of 256 offset cells with more than 22 bits. The OF table
// is an FSE table (tableLog 8) whose code-23 symbol has share cells.
func TestDecompressBlockPrefetchChoice(t *testing.T) {
	block := func(nbSeq int, share int16) []byte {
		norm := make([]int16, 24)
		norm[2], norm[23] = 256-share, share
		nc := make([]byte, 64)
		n, err := fse.WriteNCount(nc, norm, 23, 8)
		if err != nil {
			t.Fatal(err)
		}
		// no literals; nbSeq; LL RLE, OF FSE, ML RLE; a bitstream (never
		// read correctly: only the decoder choice matters).
		b := append([]byte{0x00, byte(nbSeq), 0x64, 0}, nc[:n]...)
		return append(b, 0, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x81)
	}
	for _, c := range []struct {
		hist, nbSeq int
		share       int16
		want        bool
	}{
		{1<<24 + 1, 9, 7, true},
		{1 << 24, 9, 7, false},
		{1<<24 + 1, 8, 7, false},
		{1<<24 + 1, 9, 6, false},
		{1<<24 + 1, 9, 255, true},
	} {
		d := new(dctx)
		d.decompressBegin()
		d.isFrameDecompression = false
		const dstCap = 1000
		buf := make([]byte, 0, dstCap)
		d.checkContinuity(buf, dstCap)
		d.virtualStart = dstCap - c.hist // totalHistorySize = 0 + min(dstCap, bsMax) - virtualStart
		long0 := seqLongBodyBlocks.Load()
		_, e := d.decompressBlockInternal(buf, dstCap, block(c.nbSeq, c.share), notStreaming)
		if e == dErrCorruptionDetected && d.ofTptr[0].baseValue != 8 {
			t.Fatalf("%+v: OF table not built", c)
		}
		if took := seqLongBodyBlocks.Load() != long0; took != c.want {
			t.Errorf("%+v: prefetch decoder %v, want %v (err %d)", c, took, c.want, e)
		}
	}
}

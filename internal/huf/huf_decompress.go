// Port of the single-symbol (X1) part of libzstd v1.5.7
// lib/decompress/huf_decompress.c (Copyright (c) Meta Platforms, Inc. and
// affiliates; BSD/GPLv2 dual license): the decoding table reader, the 1X and
// 4X bodies, and the fast 4X loop (HUF_decompress4X1_usingDTable_internal_fast_c_loop).
//
// The port has 64-bit semantics on every GOARCH: the 64-bit bit container
// (MEM_64bits() is true) and the fast loop is used on 32-bit targets too.
// C: huf_decompress.c:HUF_DecompressFastArgs_init returns 0 (body only) when
// MEM_32bits(); here the decoder behaves the same on every GOARCH.

package huf

import (
	"encoding/binary"
	"math/bits"

	"github.com/vahid-sohrabloo/fastzstd/internal/bitstream"
)

// decoderFastTableLog. C: HUF_DECODER_FAST_TABLELOG
const decoderFastTableLog = 11

// ErrCorruptionDetected (entropy_common.go) and ErrTableLogTooLarge
// (huf_compress.go) carry C's corruption_detected and tableLog_tooLarge;
// table description errors come from ReadStats, and bitstream.DStream.Init
// errors are passed through unchanged.

// dTableDesc. C: huf_decompress.c:DTableDesc
type dTableDesc struct {
	maxTableLog uint8
	tableType   uint8
	tableLog    uint8
	reserved    uint8
}

// DTable is a Huffman decoding table with its descriptor.
// C: HUF_DTable is one U32 array type-punned per decoder; Go keeps typed arrays.
// x1 holds HUF_DEltX1 {nbBits, byte} as nbBits | byte<<8 (the little-endian
// layout the fast loop reads as U16); x2 holds HUF_DEltX2 (Task 6).
type DTable struct {
	desc dTableDesc
	x1   [1 << TableLogMax]uint16
	x2   [1 << TableLogMax]uint32
}

// Init sets the descriptor of an empty table able to hold maxTableLog.
// C: zstd_decompress.c (hufTable[0] = maxTableLog * 0x1000001) /
// huf.h:HUF_CREATE_STATIC_DTABLEX2
func (dt *DTable) Init(maxTableLog uint) {
	dt.desc = dTableDesc{maxTableLog: uint8(maxTableLog), reserved: uint8(maxTableLog)}
}

// rescaleStats increases tableLog to targetTableLog and rescales the
// stats; a no-op if tableLog > targetTableLog. It returns the new tableLog.
// C: huf_decompress.c:HUF_rescaleStats
func rescaleStats(huffWeight []byte, rankVal []uint32, nbSymbols, tableLog, targetTableLog uint32) uint32 {
	if tableLog > targetTableLog {
		return tableLog
	}
	if tableLog < targetTableLog {
		scale := targetTableLog - tableLog
		// Increase the weight for all non-zero probability symbols by scale.
		for s := uint32(0); s < nbSymbols; s++ {
			if huffWeight[s] != 0 {
				huffWeight[s] += byte(scale)
			}
		}
		// Update rankVal to reflect the new weights. All weights except 0
		// get moved to weight + scale. Weights [1, scale] are empty.
		for s := targetTableLog; s > scale; s-- {
			rankVal[s] = rankVal[s-scale]
		}
		for s := scale; s > 0; s-- {
			rankVal[s] = 0
		}
	}
	return targetTableLog
}

// ReadDTableX1 reads a Huffman table description from src into dt and
// returns the number of bytes read. Tables are rescaled up to
// HUF_DECODER_FAST_TABLELOG when dt allows it. flags is accepted for C
// parity (HUF_flags_bmi2 only picks a compiler target in HUF_readStats).
// C: huf_decompress.c:HUF_readDTableX1_wksp (the workspace is local)
func ReadDTableX1(dt *DTable, src []byte, flags int) (int, error) {
	_ = flags
	var (
		rankVal    [TableLogMax + 1]uint32 // C: HUF_TABLELOG_ABSOLUTEMAX + 1
		rankStart  [TableLogMax + 1]uint32
		symbols    [SymbolValueMax + 1]byte
		huffWeight [SymbolValueMax + 1]byte
	)

	nbSymbols, tableLog, iSize, err := ReadStats(huffWeight[:], rankVal[:], src)
	if err != nil {
		return 0, err
	}

	// Table header
	{
		dtd := dt.desc
		maxTableLog := uint32(dtd.maxTableLog) + 1
		targetTableLog := min(maxTableLog, decoderFastTableLog)
		tableLog = rescaleStats(huffWeight[:], rankVal[:], nbSymbols, tableLog, targetTableLog)
		if tableLog > uint32(dtd.maxTableLog)+1 {
			return 0, ErrTableLogTooLarge // DTable too small, Huffman tree cannot fit in
		}
		dtd.tableType = 0
		dtd.tableLog = byte(tableLog)
		dt.desc = dtd
	}

	// Compute symbols and rankStart given rankVal: symbols holds the
	// symbols ordered by weight, rankStart the offset of each rank in the
	// DTable. (C unrolls the symbol loop by 4; same order.)
	{
		var nextRankStart uint32
		for n := 0; n < int(tableLog)+1; n++ {
			curr := nextRankStart
			nextRankStart += rankVal[n]
			rankStart[n] = curr
		}
		for n := 0; n < int(nbSymbols); n++ {
			w := huffWeight[n]
			symbols[rankStart[w]] = byte(n)
			rankStart[w]++
		}
	}

	// fill DTable: all entries of each weight in order. (C switches on
	// length to inner loops storing 1, 2 or 4 entries at a time, the latter
	// via HUF_DEltX1_set4 and MEM_write64; the stored entries are the same.)
	{
		symbol := int(rankVal[0])
		rankStart := 0
		for w := uint32(1); w < tableLog+1; w++ {
			symbolCount := int(rankVal[w])
			length := (1 << w) >> 1
			uStart := rankStart
			nbBits := uint16(tableLog + 1 - w)
			for s := 0; s < symbolCount; s++ {
				d := uint16(symbols[symbol+s])<<8 | nbBits // C: HUF_DEltX1 {nbBits, byte}
				fill := dt.x1[uStart : uStart+length]
				for u := range fill {
					fill[u] = d
				}
				uStart += length
			}
			symbol += symbolCount
			rankStart += symbolCount * length
		}
	}
	return iSize, nil
}

// decodeSymbolX1. C: huf_decompress.c:HUF_decodeSymbolX1
//
// C: BIT_lookBitsFast(Dstream, dtLog) < 1<<dtLog for dtLog >= 1, which every
// table ReadDTableX1 builds has; the index is masked to the table so that an
// unread DTable (tableLog 0, where C's read is out of range) cannot panic.
func decodeSymbolX1(bitD *bitstream.DStream, dt *[1 << TableLogMax]uint16, dtLog uint32) byte {
	val := bitD.LookBitsFast(dtLog) & (1<<TableLogMax - 1)
	e := dt[val]
	bitD.SkipBits(uint32(e & 0xFF))
	return byte(e >> 8)
}

// decodeStreamX1 decodes symbols into dst[p:pEnd] and returns pEnd-p.
// C: huf_decompress.c:HUF_decodeStreamX1 (64-bit: HUF_DECODE_SYMBOLX1_1 and
// _2 always decode, and the 32-bit-only reload loop is skipped)
func decodeStreamX1(dst []byte, p int, bitD *bitstream.DStream, pEnd int, dt *[1 << TableLogMax]uint16, dtLog uint32) int {
	pStart := p

	// up to 4 symbols at a time
	if pEnd-p > 3 {
		for bitD.Reload() == bitstream.DStreamUnfinished && p < pEnd-3 {
			dst[p] = decodeSymbolX1(bitD, dt, dtLog)
			dst[p+1] = decodeSymbolX1(bitD, dt, dtLog)
			dst[p+2] = decodeSymbolX1(bitD, dt, dtLog)
			dst[p+3] = decodeSymbolX1(bitD, dt, dtLog)
			p += 4
		}
	} else {
		bitD.Reload()
	}

	// no more data to retrieve from bitstream, no need to reload
	for ; p < pEnd; p++ {
		dst[p] = decodeSymbolX1(bitD, dt, dtLog)
	}
	return pEnd - pStart
}

// decompress1X1Body. C: huf_decompress.c:HUF_decompress1X1_usingDTable_internal_body
func decompress1X1Body(dst, src []byte, dt *DTable) error {
	var bitD bitstream.DStream
	dtLog := uint32(dt.desc.tableLog)

	if _, err := bitD.Init(src); err != nil {
		return err
	}
	decodeStreamX1(dst, 0, &bitD, len(dst), &dt.x1, dtLog)
	if !bitD.EndOfDStream() {
		return ErrCorruptionDetected
	}
	return nil
}

// Decompress1X1UsingDTable decodes the single stream src into exactly
// len(dst) bytes with an X1 table. flags is accepted for C parity
// (HUF_flags_bmi2 only picks a compiler target).
// C: huf_decompress.c:HUF_decompress1X1_usingDTable_internal
func Decompress1X1UsingDTable(dst, src []byte, dt *DTable, flags int) error {
	_ = flags
	return decompress1X1Body(dst, src, dt)
}

// decompress4X1Body. Conditions: len(dst) >= 6 (checked).
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_body
func decompress4X1Body(dst, src []byte, dt *DTable) error {
	cSrcSize := len(src)
	dstSize := len(dst)

	// Check
	if cSrcSize < 10 {
		return ErrCorruptionDetected // strict minimum : jump table + 1 byte per stream
	}
	if dstSize < 6 {
		return ErrCorruptionDetected // stream 4-split doesn't work
	}

	oend := dstSize
	olimit := oend - 3
	x1 := &dt.x1

	// Init
	var bitD1, bitD2, bitD3, bitD4 bitstream.DStream
	length1 := int(binary.LittleEndian.Uint16(src))
	length2 := int(binary.LittleEndian.Uint16(src[2:]))
	length3 := int(binary.LittleEndian.Uint16(src[4:]))
	// C: length4 is a size_t that wraps when the jump table overstates the
	// input, and then fails the length4 > cSrcSize check; here it is < 0.
	length4 := cSrcSize - (length1 + length2 + length3 + 6)
	istart1 := 6 // jumpTable
	istart2 := istart1 + length1
	istart3 := istart2 + length2
	istart4 := istart3 + length3
	segmentSize := (dstSize + 3) / 4
	opStart2 := segmentSize
	opStart3 := opStart2 + segmentSize
	opStart4 := opStart3 + segmentSize
	op1, op2, op3, op4 := 0, opStart2, opStart3, opStart4
	dtLog := uint32(dt.desc.tableLog)
	endSignal := true

	if length4 < 0 {
		return ErrCorruptionDetected // overflow
	}
	if opStart4 > oend {
		return ErrCorruptionDetected // overflow
	}
	if _, err := bitD1.Init(src[istart1:istart2]); err != nil {
		return err
	}
	if _, err := bitD2.Init(src[istart2:istart3]); err != nil {
		return err
	}
	if _, err := bitD3.Init(src[istart3:istart4]); err != nil {
		return err
	}
	if _, err := bitD4.Init(src[istart4:]); err != nil {
		return err
	}

	// up to 16 symbols per loop (4 symbols per stream) in 64-bit mode
	if oend-op4 >= 8 {
		for endSignal && op4 < olimit {
			dst[op1] = decodeSymbolX1(&bitD1, x1, dtLog)
			dst[op2] = decodeSymbolX1(&bitD2, x1, dtLog)
			dst[op3] = decodeSymbolX1(&bitD3, x1, dtLog)
			dst[op4] = decodeSymbolX1(&bitD4, x1, dtLog)
			dst[op1+1] = decodeSymbolX1(&bitD1, x1, dtLog)
			dst[op2+1] = decodeSymbolX1(&bitD2, x1, dtLog)
			dst[op3+1] = decodeSymbolX1(&bitD3, x1, dtLog)
			dst[op4+1] = decodeSymbolX1(&bitD4, x1, dtLog)
			dst[op1+2] = decodeSymbolX1(&bitD1, x1, dtLog)
			dst[op2+2] = decodeSymbolX1(&bitD2, x1, dtLog)
			dst[op3+2] = decodeSymbolX1(&bitD3, x1, dtLog)
			dst[op4+2] = decodeSymbolX1(&bitD4, x1, dtLog)
			dst[op1+3] = decodeSymbolX1(&bitD1, x1, dtLog)
			dst[op2+3] = decodeSymbolX1(&bitD2, x1, dtLog)
			dst[op3+3] = decodeSymbolX1(&bitD3, x1, dtLog)
			dst[op4+3] = decodeSymbolX1(&bitD4, x1, dtLog)
			op1 += 4
			op2 += 4
			op3 += 4
			op4 += 4
			// C: endSignal &= ... — all four reloads run every iteration
			e1 := bitD1.ReloadFast() == bitstream.DStreamUnfinished
			e2 := bitD2.ReloadFast() == bitstream.DStreamUnfinished
			e3 := bitD3.ReloadFast() == bitstream.DStreamUnfinished
			e4 := bitD4.ReloadFast() == bitstream.DStreamUnfinished
			endSignal = e1 && e2 && e3 && e4
		}
	}

	// check corruption
	if op1 > opStart2 {
		return ErrCorruptionDetected
	}
	if op2 > opStart3 {
		return ErrCorruptionDetected
	}
	if op3 > opStart4 {
		return ErrCorruptionDetected
	}
	// note : op4 supposed already verified within main loop

	// finish bitStreams one by one
	decodeStreamX1(dst, op1, &bitD1, opStart2, x1, dtLog)
	decodeStreamX1(dst, op2, &bitD2, opStart3, x1, dtLog)
	decodeStreamX1(dst, op3, &bitD3, opStart4, x1, dtLog)
	decodeStreamX1(dst, op4, &bitD4, oend, x1, dtLog)

	// check
	if !(bitD1.EndOfDStream() && bitD2.EndOfDStream() && bitD3.EndOfDStream() && bitD4.EndOfDStream()) {
		return ErrCorruptionDetected
	}
	return nil
}

// decompressFastArgs holds the fast loop's state: offsets into src (ip,
// iend; C's ilowest is offset 0) and into dst (op, oend).
// C: huf_decompress.c:HUF_DecompressFastArgs
type decompressFastArgs struct {
	ip   [4]int
	op   [4]int
	bits [4]uint64
	dt   *[1 << TableLogMax]uint16
	oend int
	iend [4]int
	src  []byte
	dst  []byte
}

// initFastDStream. C: huf_decompress.c:HUF_initFastDStream
func initFastDStream(ip []byte) uint64 {
	lastByte := ip[7]
	var bitsConsumed uint
	if lastByte != 0 {
		bitsConsumed = 8 - uint(highbit32(uint32(lastByte)))
	}
	value := binary.LittleEndian.Uint64(ip) | 1
	return value << bitsConsumed
}

// decompressFastArgsInit initializes args for the fast decoding loop. It
// returns 1 on success, 0 if the fallback implementation should be used, or
// an error. C: huf_decompress.c:HUF_DecompressFastArgs_init (without its
// MEM_32bits() fallback; see the file comment)
func decompressFastArgsInit(args *decompressFastArgs, dst, src []byte, dt *DTable) (int, error) {
	dtLog := uint32(dt.desc.tableLog)
	srcSize := len(src)
	dstSize := len(dst)
	oend := dstSize

	// Avoid nullptr addition
	if dstSize == 0 {
		return 0, nil
	}

	// strict minimum : jump table + 1 byte per stream
	if srcSize < 10 {
		return 0, ErrCorruptionDetected
	}

	// Must have at least 8 bytes per stream because we don't handle
	// initializing smaller bit containers. If table log is not correct at
	// this point, fallback to the old decoder. On small inputs we don't have
	// enough data to trigger the fast loop, so use the old decoder.
	if dtLog != decoderFastTableLog {
		return 0, nil
	}

	// Read the jump table.
	{
		length1 := int(binary.LittleEndian.Uint16(src))
		length2 := int(binary.LittleEndian.Uint16(src[2:]))
		length3 := int(binary.LittleEndian.Uint16(src[4:]))
		length4 := srcSize - (length1 + length2 + length3 + 6)
		args.iend[0] = 6 // jumpTable
		args.iend[1] = args.iend[0] + length1
		args.iend[2] = args.iend[1] + length2
		args.iend[3] = args.iend[2] + length3

		// HUF_initFastDStream() requires this, and this small of an input
		// won't benefit from the ASM loop anyways.
		// C: length4 is a size_t; a negative value wraps to a huge one, so
		// it passes the < 8 test and fails the overflow test.
		if length1 < 8 || length2 < 8 || length3 < 8 || (length4 >= 0 && length4 < 8) {
			return 0, nil
		}
		if length4 < 0 {
			return 0, ErrCorruptionDetected // overflow
		}
	}
	// ip[] contains the position that is currently loaded into bits[].
	args.ip[0] = args.iend[1] - 8
	args.ip[1] = args.iend[2] - 8
	args.ip[2] = args.iend[3] - 8
	args.ip[3] = srcSize - 8

	// op[] contains the output pointers.
	args.op[0] = 0
	args.op[1] = args.op[0] + (dstSize+3)/4
	args.op[2] = args.op[1] + (dstSize+3)/4
	args.op[3] = args.op[2] + (dstSize+3)/4

	// No point to call the ASM loop for tiny outputs.
	if args.op[3] >= oend {
		return 0, nil
	}

	// bits[] is the bit container. It is read from the MSB down to the LSB.
	// It is shifted left as it is read, and zeros are shifted in. After the
	// lowest valid bit a 1 is set, so that CountTrailingZeros(bits[]) can be
	// used to count how many bits we've consumed.
	for i := range args.bits {
		args.bits[i] = initFastDStream(src[args.ip[i]:])
	}

	// The decoders must be sure to never read beyond ilowest (offset 0).
	args.oend = oend
	args.dt = &dt.x1
	args.src = src
	args.dst = dst
	return 1, nil
}

// initRemainingDStream hands stream back to a BIT_DStream_t over the whole
// input (C: start = ilowest). C: huf_decompress.c:HUF_initRemainingDStream
func initRemainingDStream(bit *bitstream.DStream, args *decompressFastArgs, stream int, segmentEnd int) error {
	// Validate that we haven't overwritten.
	if args.op[stream] > segmentEnd {
		return ErrCorruptionDetected
	}
	// Validate that we haven't read beyond iend[]. Note that ip[] may be <
	// iend[] because the MSB is the next bit to read, and we may have
	// consumed 100% of the stream, so down to iend[i] - 8 is valid.
	if args.ip[stream] < args.iend[stream]-8 {
		return ErrCorruptionDetected
	}

	// Construct the BIT_DStream_t.
	ip := args.ip[stream]
	bit.SetRaw(args.src, ip, binary.LittleEndian.Uint64(args.src[ip:]), uint32(bits.TrailingZeros64(args.bits[stream])))
	return nil
}

// decompress4X1FastCLoop decodes 5 symbols per stream per iteration while
// no bound can be hit. C:
// huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast_c_loop, with
// offsets in place of pointers. Each iteration reads at most 7 bytes below
// ip[i] (5 symbols * 11 bits plus fewer than 8 unconsumed bits) and writes 5
// bytes at op[i]; iters bounds both against ilowest (0) and oend.
func decompress4X1FastCLoop(args *decompressFastArgs) {
	bits4 := args.bits
	ip := args.ip
	op := args.op
	dtable := args.dt
	oend := args.oend
	src := args.src
	dst := args.dst

	for {
		// Compute olimit
		// Each iteration produces 5 output symbols per stream.
		oiters := (oend - op[3]) / 5
		// Each iteration consumes up to 11 bits * 5 = 55 bits < 7 bytes per
		// stream.
		iiters := ip[0] / 7 // C: (ip[0] - ilowest) / 7
		// We can safely run iters iterations before running bounds checks.
		iters := min(oiters, iiters)
		symbols := iters * 5

		// We can simply check that op[3] < olimit, instead of checking all
		// of our bounds, since we can't hit the other bounds until we've
		// run iters iterations, which only happens when op[3] == olimit.
		olimit := op[3] + symbols

		// Exit fast decoding loop once we reach the end.
		if op[3] == olimit {
			break
		}

		// Exit the decoding loop if any input pointer has crossed the
		// previous one. This indicates corruption, and a precondition to
		// our loop is that ip[i] >= ip[0].
		if ip[1] < ip[0] || ip[2] < ip[1] || ip[3] < ip[2] {
			break // C: goto _out
		}

		for {
			// Decode 5 symbols in each of the 4 streams.
			// C: HUF_4X1_DECODE_SYMBOL decodes symbol-major across the
			// streams; the streams are independent, so stream-major is
			// equivalent.
			for stream := 0; stream < 4; stream++ {
				b := bits4[stream]
				o := dst[op[stream] : op[stream]+5]
				for symbol := 0; symbol < 5; symbol++ {
					entry := dtable[b>>53]
					b <<= entry & 0x3F
					o[symbol] = byte(entry >> 8)
				}
				bits4[stream] = b
			}

			// Reload each of the 4 the bitstreams. C: HUF_4X1_RELOAD_STREAM
			for stream := 0; stream < 4; stream++ {
				ctz := bits.TrailingZeros64(bits4[stream])
				nbBits := ctz & 7
				nbBytes := ctz >> 3
				op[stream] += 5
				ip[stream] -= nbBytes
				bits4[stream] = binary.LittleEndian.Uint64(src[ip[stream]:]) | 1
				bits4[stream] <<= nbBits
			}
			if op[3] >= olimit {
				break
			}
		}
	}

	// Save the final values of each of the state variables back to args.
	args.bits = bits4
	args.ip = ip
	args.op = op
}

// decompress4X1Fast returns len(dst) on success, 0 if the fallback
// implementation should be used, or an error.
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast (loopFn
// is always the C loop: there is no assembly yet)
func decompress4X1Fast(dst, src []byte, dt *DTable) (int, error) {
	var args decompressFastArgs
	if ret, err := decompressFastArgsInit(&args, dst, src, dt); err != nil || ret == 0 {
		return 0, err
	}

	decompress4X1FastCLoop(&args)

	// Our loop guarantees that ip[] >= ilowest and that we haven't
	// overwritten any op[].

	// finish bit streams one by one.
	dstSize := len(dst)
	oend := dstSize
	segmentSize := (dstSize + 3) / 4
	segmentEnd := 0
	for i := 0; i < 4; i++ {
		var bit bitstream.DStream
		if segmentSize <= oend-segmentEnd {
			segmentEnd += segmentSize
		} else {
			segmentEnd = oend
		}
		if err := initRemainingDStream(&bit, &args, i, segmentEnd); err != nil {
			return 0, err
		}
		// Decompress and validate that we've produced exactly the expected
		// length.
		args.op[i] += decodeStreamX1(dst, args.op[i], &bit, segmentEnd, &dt.x1, decoderFastTableLog)
		if args.op[i] != segmentEnd {
			return 0, ErrCorruptionDetected
		}
	}

	// decoded size
	return dstSize, nil
}

// Decompress4X1UsingDTable decodes the four streams of src (6-byte jump
// table first) into exactly len(dst) bytes with an X1 table. The fast loop
// runs unless flags has FlagDisableFast, falling back to the body when it
// declines (table log other than 11, streams under 8 bytes, tiny output).
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal — with
// DYNAMIC_BMI2 (x86-64) C runs only the body when HUF_flags_bmi2 is unset;
// here FlagBMI2 is ignored and the decoder behaves as C on a BMI2 CPU (or on
// targets without DYNAMIC_BMI2). FlagDisableAsm is accepted: there is no
// assembly loop yet.
func Decompress4X1UsingDTable(dst, src []byte, dt *DTable, flags int) error {
	if flags&FlagDisableFast == 0 {
		ret, err := decompress4X1Fast(dst, src, dt)
		if err != nil {
			return err
		}
		if ret != 0 {
			return nil
		}
	}
	return decompress4X1Body(dst, src, dt)
}

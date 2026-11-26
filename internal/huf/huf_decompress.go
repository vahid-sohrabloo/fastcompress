// Port of libzstd v1.5.7 lib/decompress/huf_decompress.c (Copyright (c)
// Meta Platforms, Inc. and affiliates; BSD/GPLv2 dual license): for the
// single-symbol (X1) and double-symbol (X2) decoders, the decoding table
// reader, the 1X and 4X bodies and the fast 4X path (its loops are in
// huf_decompress_generic.go and huf_decompress_{amd64,arm64}.s); then the
// decoder selector and the entry points the literals decoder calls.
//
// The port has 64-bit semantics on every GOARCH: the 64-bit bit container
// (MEM_64bits() is true) and the fast loop is used on 32-bit targets too.
// C: huf_decompress.c:HUF_DecompressFastArgs_init returns 0 (body only) when
// MEM_32bits(); here the decoder behaves the same on every GOARCH.

package huf

import (
	"encoding/binary"
	"math/bits"

	"github.com/vahid-sohrabloo/fastcompress/internal/bitstream"
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
// layout the fast loop reads as U16); x2 holds HUF_DEltX2 {sequence,
// nbBits, length} as sequence | nbBits<<16 | length<<24. desc.tableType
// says which one the last read filled (0: x1, 1: x2); the other is stale.
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

// set4 is the entry {nbBits, sym} four times over.
// C: huf_decompress.c:HUF_DEltX1_set4
func set4(sym byte, nbBits uint16) uint64 {
	return uint64(uint16(sym)<<8|nbBits) * 0x0001_0001_0001_0001
}

// store4 stores the four entries of d4. The compiler merges the stores into
// one of 64 bits where the target allows it (amd64, arm64).
// C: MEM_write64
func store4(f *[4]uint16, d4 uint64) {
	f[0] = uint16(d4)
	f[1] = uint16(d4 >> 16)
	f[2] = uint16(d4 >> 32)
	f[3] = uint16(d4 >> 48)
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
	// DTable.
	{
		var nextRankStart uint32
		for n := 0; n < int(tableLog)+1; n++ {
			curr := nextRankStart
			nextRankStart += rankVal[n]
			rankStart[n] = curr
		}
		n := 0
		for ; n+4 <= int(nbSymbols); n += 4 {
			// ReadStats leaves no weight above TableLogMax, so the masks
			// only spare the bounds checks.
			for u, w := range [4]byte(huffWeight[n : n+4]) {
				r := &rankStart[w&0xF]
				symbols[byte(*r)] = byte(n + u)
				*r++
			}
		}
		for ; n < int(nbSymbols); n++ {
			w := huffWeight[n] & 0xF
			symbols[byte(rankStart[w])] = byte(n)
			rankStart[w]++
		}
	}

	// fill DTable: all entries of each weight in order. That way length is
	// a constant for each iteration of the outer loop, and the switch picks
	// an inner loop for it.
	{
		symbol := int(rankVal[0])
		rankStart := 0
		for w := uint32(1); w < tableLog+1; w++ {
			symbolCount := int(rankVal[w])
			length := (1 << w) >> 1
			nbBits := uint16(tableLog + 1 - w)
			syms := symbols[symbol : symbol+symbolCount]
			fill := dt.x1[rankStart : rankStart+symbolCount*length]
			switch length {
			case 1:
				for s, sym := range syms {
					fill[s] = uint16(sym)<<8 | nbBits // C: HUF_DEltX1 {nbBits, byte}
				}
			case 2:
				for _, sym := range syms {
					d := uint16(sym)<<8 | nbBits
					fill[0], fill[1] = d, d
					fill = fill[2:]
				}
			case 4:
				for _, sym := range syms {
					store4((*[4]uint16)(fill), set4(sym, nbBits))
					fill = fill[4:]
				}
			case 8:
				for _, sym := range syms {
					d4 := set4(sym, nbBits)
					store4((*[4]uint16)(fill), d4)
					store4((*[4]uint16)(fill[4:]), d4)
					fill = fill[8:]
				}
			default: // length is a power of 2, >= 16
				for _, sym := range syms {
					d4 := set4(sym, nbBits)
					for u := 0; u < length; u += 16 {
						f := (*[16]uint16)(fill[u:])
						store4((*[4]uint16)(f[0:4]), d4)
						store4((*[4]uint16)(f[4:8]), d4)
						store4((*[4]uint16)(f[8:12]), d4)
						store4((*[4]uint16)(f[12:16]), d4)
					}
					fill = fill[length:]
				}
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
	dt   *[1 << TableLogMax]uint16 // X1 table
	dtX2 *[1 << TableLogMax]uint32 // X2 table (C: dt is untyped)
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
	args.dtX2 = &dt.x2
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

// decompress4X1Fast returns len(dst) on success, 0 if the fallback
// implementation should be used, or an error.
// C: huf_decompress.c:HUF_decompress4X1_usingDTable_internal_fast (loopFn:
// see decompress4X1FastLoop)
func decompress4X1Fast(dst, src []byte, dt *DTable, flags int) (int, error) {
	var args decompressFastArgs
	if ret, err := decompressFastArgsInit(&args, dst, src, dt); err != nil || ret == 0 {
		return 0, err
	}

	decompress4X1FastLoop(&args, dt, flags)

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
// targets without DYNAMIC_BMI2). The fast loop is the assembly (amd64 with
// BMI2, or arm64) when flags lacks FlagDisableAsm, else the Go loop.
func Decompress4X1UsingDTable(dst, src []byte, dt *DTable, flags int) error {
	if flags&FlagDisableFast == 0 {
		ret, err := decompress4X1Fast(dst, src, dt, flags)
		if err != nil {
			return err
		}
		if ret != 0 {
			return nil
		}
	}
	return decompress4X1Body(dst, src, dt)
}

// Decompress1X1DCtxWksp reads an X1 table from src and decodes the single
// stream that follows it (ZSTD_decodeLiteralsBlock uses it for a
// single-stream literals section). C: huf_decompress.c:HUF_decompress1X1_DCtx_wksp
func Decompress1X1DCtxWksp(dt *DTable, dst, src []byte, flags int) error {
	hSize, err := ReadDTableX1(dt, src, flags)
	if err != nil {
		return err
	}
	if hSize >= len(src) {
		return ErrSrcSizeWrong
	}
	return Decompress1X1UsingDTable(dst, src[hSize:], dt, flags)
}

// decompress4X1DCtxWksp reads an X1 table from src and decodes the four
// streams that follow it. C: huf_decompress.c:HUF_decompress4X1_DCtx_wksp
func decompress4X1DCtxWksp(dt *DTable, dst, src []byte, flags int) error {
	hSize, err := ReadDTableX1(dt, src, flags)
	if err != nil {
		return err
	}
	if hSize >= len(src) {
		return ErrSrcSizeWrong
	}
	return Decompress4X1UsingDTable(dst, src[hSize:], dt, flags)
}

/* *************************/
/* double-symbols decoding */
/* *************************/

// buildDEltX2U32 constructs a HUF_DEltX2 (little-endian layout).
// C: huf_decompress.c:HUF_buildDEltX2U32
func buildDEltX2U32(symbol, nbBits, baseSeq uint32, level int) uint32 {
	seq := symbol
	if level != 1 {
		seq = baseSeq + symbol<<8
	}
	return seq + nbBits<<16 + uint32(level)<<24
}

// fillDTableX2ForWeight fills the DTable rank (dtRank) with all the
// symbols of sorted that are each nbBits long. baseSeq is 0 at level 1,
// else the first level symbol. C: huf_decompress.c:HUF_fillDTableX2ForWeight
// (C switches on length to store 1, 2, 4, 8 or 8n entries at a time; the
// stored entries are the same)
func fillDTableX2ForWeight(dtRank []uint32, sorted []byte, nbBits, tableLog uint32, baseSeq uint32, level int) {
	length := 1 << ((tableLog - nbBits) & 0x1F)
	for _, symbol := range sorted {
		d := buildDEltX2U32(uint32(symbol), nbBits, baseSeq, level)
		fill := dtRank[:length]
		for u := range fill {
			fill[u] = d
		}
		dtRank = dtRank[length:]
	}
}

// fillDTableX2Level2 fills the 1<<(targetLog-consumedBits) entries of
// dtable whose first symbol is baseSeq. rankVal must hold at least
// HUF_TABLELOG_MAX+1 entries. C: huf_decompress.c:HUF_fillDTableX2Level2
func fillDTableX2Level2(dtable []uint32, targetLog, consumedBits uint32, rankVal []uint32, minWeight, maxWeight1 int,
	sortedSymbols []byte, rankStart []uint32, nbBitsBaseline uint32, baseSeq uint32) {
	// Fill skipped values (all positions up to rankVal[minWeight]). These
	// are positions only get a single symbol because the combined weight
	// is too large.
	if minWeight > 1 {
		length := 1 << ((targetLog - consumedBits) & 0x1F)
		d := buildDEltX2U32(baseSeq, consumedBits, 0, 1)
		skipSize := int(rankVal[minWeight])
		// C: case 2 and case 4 store 2 and 4 entries; the default stores
		// skipSize rounded up to 8 (all within length).
		n := length
		if length > 4 {
			n = (skipSize + 7) &^ 7
		}
		fill := dtable[:n]
		for i := range fill {
			fill[i] = d
		}
	}

	// Fill each of the second level symbols by weight.
	for w := minWeight; w < maxWeight1; w++ {
		begin := int(rankStart[w])
		end := int(rankStart[w+1])
		nbBits := nbBitsBaseline - uint32(w)
		totalBits := nbBits + consumedBits
		fillDTableX2ForWeight(dtable[rankVal[w]:], sortedSymbols[begin:end], totalBits, targetLog, baseSeq, 2)
	}
}

// fillDTableX2. C: huf_decompress.c:HUF_fillDTableX2
func fillDTableX2(dtable []uint32, targetLog uint32, sortedList []byte, rankStart []uint32,
	rankValOrigin *[TableLogMax][TableLogMax + 1]uint32, maxWeight, nbBitsBaseline uint32) {
	rankVal := rankValOrigin[0][:]
	scaleLog := int(nbBitsBaseline) - int(targetLog) // note : targetLog >= srcLog, hence scaleLog <= 1
	minBits := nbBitsBaseline - maxWeight
	wEnd := int(maxWeight) + 1

	// Fill DTable in order of weight.
	for w := 1; w < wEnd; w++ {
		begin := int(rankStart[w])
		end := int(rankStart[w+1])
		nbBits := nbBitsBaseline - uint32(w)

		if targetLog-nbBits >= minBits {
			// Enough room for a second symbol.
			start := int(rankVal[w])
			length := 1 << ((targetLog - nbBits) & 0x1F)
			minWeight := int(nbBits) + scaleLog
			if minWeight < 1 {
				minWeight = 1
			}
			// Fill the DTable for every symbol of weight w. These symbols
			// get at least 1 second symbol.
			for s := begin; s != end; s++ {
				fillDTableX2Level2(dtable[start:], targetLog, nbBits,
					rankValOrigin[nbBits][:], minWeight, wEnd,
					sortedList, rankStart,
					nbBitsBaseline, uint32(sortedList[s]))
				start += length
			}
		} else {
			// Only a single symbol.
			fillDTableX2ForWeight(dtable[rankVal[w]:], sortedList[begin:end], nbBits, targetLog, 0, 1)
		}
	}
}

// ReadDTableX2 reads a Huffman table description from src into dt as a
// double-symbol table and returns the number of bytes read. Tables up to
// HUF_DECODER_FAST_TABLELOG bits are built at that log when dt allows
// more. flags is accepted for C parity.
// C: huf_decompress.c:HUF_readDTableX2_wksp (the workspace is local)
func ReadDTableX2(dt *DTable, src []byte, flags int) (int, error) {
	_ = flags
	var (
		rankVal      [TableLogMax][TableLogMax + 1]uint32 // C: rankVal_t
		rankStats    [TableLogMax + 1]uint32
		rankStart0   [TableLogMax + 3]uint32
		sortedSymbol [SymbolValueMax + 1]byte
		weightList   [SymbolValueMax + 1]byte
	)
	dtd := dt.desc
	maxTableLog := uint32(dtd.maxTableLog)
	rankStart := rankStart0[1:]

	if maxTableLog > TableLogMax {
		return 0, ErrTableLogTooLarge
	}

	nbSymbols, tableLog, iSize, err := ReadStats(weightList[:], rankStats[:], src)
	if err != nil {
		return 0, err
	}

	// check result
	if tableLog > maxTableLog {
		return 0, ErrTableLogTooLarge // DTable can't fit code depth
	}
	if tableLog <= decoderFastTableLog && maxTableLog > decoderFastTableLog {
		maxTableLog = decoderFastTableLog
	}

	// find maxWeight
	maxW := tableLog
	for rankStats[maxW] == 0 { // necessarily finds a solution before 0
		maxW--
	}

	// Get start index of each weight
	{
		var nextRankStart uint32
		for w := uint32(1); w < maxW+1; w++ {
			curr := nextRankStart
			nextRankStart += rankStats[w]
			rankStart[w] = curr
		}
		rankStart[0] = nextRankStart // put all 0w symbols at the end of sorted list
		rankStart[maxW+1] = nextRankStart
	}

	// sort symbols by weight
	{
		for s := uint32(0); s < nbSymbols; s++ {
			w := weightList[s]
			r := rankStart[w]
			rankStart[w]++
			sortedSymbol[r] = byte(s)
		}
		rankStart[0] = 0 // forget 0w symbols; this is beginning of weight(1)
	}

	// Build rankVal
	{
		rankVal0 := &rankVal[0]
		{
			rescale := int(maxTableLog-tableLog) - 1 // tableLog <= maxTableLog
			var nextRankVal uint32
			for w := uint32(1); w < maxW+1; w++ {
				curr := nextRankVal
				nextRankVal += rankStats[w] << (int(w) + rescale)
				rankVal0[w] = curr
			}
		}
		{
			minBits := tableLog + 1 - maxW
			for consumed := minBits; consumed < maxTableLog-minBits+1; consumed++ {
				rankValPtr := &rankVal[consumed]
				for w := uint32(1); w < maxW+1; w++ {
					rankValPtr[w] = rankVal0[w] >> consumed
				}
			}
		}
	}

	fillDTableX2(dt.x2[:], maxTableLog, sortedSymbol[:], rankStart0[:], &rankVal, maxW, tableLog+1)

	dtd.tableLog = byte(maxTableLog)
	dtd.tableType = 1
	dt.desc = dtd
	return iSize, nil
}

// decodeSymbolX2 writes the entry's 2-byte sequence at dst[p:] and returns
// its length (1 or 2). C: huf_decompress.c:HUF_decodeSymbolX2
//
// The index is masked to the table as in decodeSymbolX1 (a no-op for
// every table ReadDTableX2 builds, which have dtLog >= 1).
func decodeSymbolX2(dst []byte, p int, bitD *bitstream.DStream, dt *[1 << TableLogMax]uint32, dtLog uint32) int {
	val := bitD.LookBitsFast(dtLog) & (1<<TableLogMax - 1)
	e := dt[val]
	dst[p] = byte(e) // C: ZSTD_memcpy(op, &dt[val].sequence, 2)
	dst[p+1] = byte(e >> 8)
	bitD.SkipBits(e >> 16 & 0xFF)
	return int(e >> 24)
}

// decodeLastSymbolX2. C: huf_decompress.c:HUF_decodeLastSymbolX2
func decodeLastSymbolX2(dst []byte, p int, bitD *bitstream.DStream, dt *[1 << TableLogMax]uint32, dtLog uint32) int {
	val := bitD.LookBitsFast(dtLog) & (1<<TableLogMax - 1)
	e := dt[val]
	dst[p] = byte(e)
	if e>>24 == 1 {
		bitD.SkipBits(e >> 16 & 0xFF)
	} else if bitD.BitsConsumed() < 64 {
		bitD.SkipBits(e >> 16 & 0xFF)
		if bitD.BitsConsumed() > 64 {
			// ugly hack; works only because it's the last symbol. Note :
			// can't easily extract nbBits from just this symbol
			bitD.SetBitsConsumed(64)
		}
	}
	return 1
}

// decodeStreamX2 decodes symbols into dst[p:pEnd] and returns pEnd-p.
// C: huf_decompress.c:HUF_decodeStreamX2 (64-bit: HUF_DECODE_SYMBOLX2_1 and
// _2 always decode)
func decodeStreamX2(dst []byte, p int, bitD *bitstream.DStream, pEnd int, dt *[1 << TableLogMax]uint32, dtLog uint32) int {
	pStart := p

	// up to 8 symbols at a time
	if pEnd-p >= 8 {
		if dtLog <= 11 {
			// up to 10 symbols at a time
			for bitD.Reload() == bitstream.DStreamUnfinished && p < pEnd-9 {
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
			}
		} else {
			// up to 8 symbols at a time
			for bitD.Reload() == bitstream.DStreamUnfinished && p < pEnd-7 {
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
				p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
			}
		}
	} else {
		bitD.Reload()
	}

	// closer to end : up to 2 symbols at a time
	if pEnd-p >= 2 {
		for bitD.Reload() == bitstream.DStreamUnfinished && p <= pEnd-2 {
			p += decodeSymbolX2(dst, p, bitD, dt, dtLog)
		}
		for p <= pEnd-2 {
			p += decodeSymbolX2(dst, p, bitD, dt, dtLog) // no need to reload : reached the end of DStream
		}
	}

	if p < pEnd {
		p += decodeLastSymbolX2(dst, p, bitD, dt, dtLog)
	}
	return p - pStart
}

// decompress1X2Body. C: huf_decompress.c:HUF_decompress1X2_usingDTable_internal_body
func decompress1X2Body(dst, src []byte, dt *DTable) error {
	var bitD bitstream.DStream

	// Init
	if _, err := bitD.Init(src); err != nil {
		return err
	}

	// decode
	decodeStreamX2(dst, 0, &bitD, len(dst), &dt.x2, uint32(dt.desc.tableLog))

	// check
	if !bitD.EndOfDStream() {
		return ErrCorruptionDetected
	}
	return nil
}

// Decompress1X2UsingDTable decodes the single stream src into exactly
// len(dst) bytes with a table read by ReadDTableX2. Precondition (as in
// C): dt must have been filled by a successful ReadDTableX2; on a DTable
// that never was, the zero entries (length 0) make it loop forever. flags is accepted for
// C parity (HUF_flags_bmi2 only picks a compiler target).
// C: huf_decompress.c:HUF_decompress1X2_usingDTable_internal
func Decompress1X2UsingDTable(dst, src []byte, dt *DTable, flags int) error {
	_ = flags
	return decompress1X2Body(dst, src, dt)
}

// decompress4X2Body. Conditions: len(dst) >= 6 (checked).
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal_body (the
// non-clang decode order; the streams are independent, so the clang order
// gives the same output)
//
// Bounds: each iteration writes at most 8 bytes per stream (4 entries of
// 2 bytes). op4 < oend-7 bounds stream 4; op3, the fastest-growing of the
// others relative to its start, stays below oend-6 because the iteration
// count is at most (oend-7-opStart4)/4+1 and 4*segmentSize >= dstSize.
func decompress4X2Body(dst, src []byte, dt *DTable) error {
	cSrcSize := len(src)
	dstSize := len(dst)

	if cSrcSize < 10 {
		return ErrCorruptionDetected // strict minimum : jump table + 1 byte per stream
	}
	if dstSize < 6 {
		return ErrCorruptionDetected // stream 4-split doesn't work
	}

	oend := dstSize
	olimit := oend - 7 // C: oend - (sizeof(size_t)-1)
	x2 := &dt.x2

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
	endSignal := true
	dtLog := uint32(dt.desc.tableLog)

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

	// 16-32 symbols per loop (4-8 symbols per stream)
	if oend-op4 >= 8 {
		for endSignal && op4 < olimit {
			op1 += decodeSymbolX2(dst, op1, &bitD1, x2, dtLog)
			op2 += decodeSymbolX2(dst, op2, &bitD2, x2, dtLog)
			op3 += decodeSymbolX2(dst, op3, &bitD3, x2, dtLog)
			op4 += decodeSymbolX2(dst, op4, &bitD4, x2, dtLog)
			op1 += decodeSymbolX2(dst, op1, &bitD1, x2, dtLog)
			op2 += decodeSymbolX2(dst, op2, &bitD2, x2, dtLog)
			op3 += decodeSymbolX2(dst, op3, &bitD3, x2, dtLog)
			op4 += decodeSymbolX2(dst, op4, &bitD4, x2, dtLog)
			op1 += decodeSymbolX2(dst, op1, &bitD1, x2, dtLog)
			op2 += decodeSymbolX2(dst, op2, &bitD2, x2, dtLog)
			op3 += decodeSymbolX2(dst, op3, &bitD3, x2, dtLog)
			op4 += decodeSymbolX2(dst, op4, &bitD4, x2, dtLog)
			op1 += decodeSymbolX2(dst, op1, &bitD1, x2, dtLog)
			op2 += decodeSymbolX2(dst, op2, &bitD2, x2, dtLog)
			op3 += decodeSymbolX2(dst, op3, &bitD3, x2, dtLog)
			op4 += decodeSymbolX2(dst, op4, &bitD4, x2, dtLog)
			// C: endSignal = ... & ... — all four reloads run every iteration
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
	// note : op4 already verified within main loop

	// finish bitStreams one by one
	decodeStreamX2(dst, op1, &bitD1, opStart2, x2, dtLog)
	decodeStreamX2(dst, op2, &bitD2, opStart3, x2, dtLog)
	decodeStreamX2(dst, op3, &bitD3, opStart4, x2, dtLog)
	decodeStreamX2(dst, op4, &bitD4, oend, x2, dtLog)

	// check
	if !(bitD1.EndOfDStream() && bitD2.EndOfDStream() && bitD3.EndOfDStream() && bitD4.EndOfDStream()) {
		return ErrCorruptionDetected
	}
	return nil
}

// decompress4X2Fast returns len(dst) on success, 0 if the fallback
// implementation should be used, or an error.
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal_fast (loopFn:
// see decompress4X2FastLoop)
func decompress4X2Fast(dst, src []byte, dt *DTable, flags int) (int, error) {
	var args decompressFastArgs
	if ret, err := decompressFastArgsInit(&args, dst, src, dt); err != nil || ret == 0 {
		return 0, err
	}

	decompress4X2FastLoop(&args, dt, flags)

	// note : op4 already verified within main loop

	// finish bitStreams one by one
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
		args.op[i] += decodeStreamX2(dst, args.op[i], &bit, segmentEnd, &dt.x2, decoderFastTableLog)
		if args.op[i] != segmentEnd {
			return 0, ErrCorruptionDetected
		}
	}

	// decoded size
	return dstSize, nil
}

// Decompress4X2UsingDTable decodes the four streams of src (6-byte jump
// table first) into exactly len(dst) bytes with a table read by
// ReadDTableX2. Flags as for Decompress4X1UsingDTable. Precondition (as in
// C): dt must have been filled by a successful ReadDTableX2; on a DTable
// that never was, the zero entries (length 0) make it loop forever.
// C: huf_decompress.c:HUF_decompress4X2_usingDTable_internal
func Decompress4X2UsingDTable(dst, src []byte, dt *DTable, flags int) error {
	if flags&FlagDisableFast == 0 {
		ret, err := decompress4X2Fast(dst, src, dt, flags)
		if err != nil {
			return err
		}
		if ret != 0 {
			return nil
		}
	}
	return decompress4X2Body(dst, src, dt)
}

// decompress1X2DCtxWksp reads an X2 table from src and decodes the single
// stream that follows it. C: huf_decompress.c:HUF_decompress1X2_DCtx_wksp
func decompress1X2DCtxWksp(dt *DTable, dst, src []byte, flags int) error {
	hSize, err := ReadDTableX2(dt, src, flags)
	if err != nil {
		return err
	}
	if hSize >= len(src) {
		return ErrSrcSizeWrong
	}
	return Decompress1X2UsingDTable(dst, src[hSize:], dt, flags)
}

// decompress4X2DCtxWksp reads an X2 table from src and decodes the four
// streams that follow it. C: huf_decompress.c:HUF_decompress4X2_DCtx_wksp
func decompress4X2DCtxWksp(dt *DTable, dst, src []byte, flags int) error {
	hSize, err := ReadDTableX2(dt, src, flags)
	if err != nil {
		return err
	}
	if hSize >= len(src) {
		return ErrSrcSizeWrong
	}
	return Decompress4X2UsingDTable(dst, src[hSize:], dt, flags)
}

/* ***********************************/
/* Universal decompression selectors */
/* ***********************************/

// algoTimeT. C: huf_decompress.c:algo_time_t
type algoTimeT struct {
	tableTime     uint32
	decode256Time uint32
}

// algoTime. C: huf_decompress.c:algoTime
var algoTime = [16][2]algoTimeT{ // [Quantization][single, double]
	// single, double, quad
	{{0, 0}, {1, 1}},           // Q==0 : impossible
	{{0, 0}, {1, 1}},           // Q==1 : impossible
	{{150, 216}, {381, 119}},   // Q == 2 : 12-18%
	{{170, 205}, {514, 112}},   // Q == 3 : 18-25%
	{{177, 199}, {539, 110}},   // Q == 4 : 25-32%
	{{197, 194}, {644, 107}},   // Q == 5 : 32-38%
	{{221, 192}, {735, 107}},   // Q == 6 : 38-44%
	{{256, 189}, {881, 106}},   // Q == 7 : 44-50%
	{{359, 188}, {1167, 109}},  // Q == 8 : 50-56%
	{{582, 187}, {1570, 114}},  // Q == 9 : 56-62%
	{{688, 187}, {1712, 122}},  // Q ==10 : 62-69%
	{{825, 186}, {1965, 136}},  // Q ==11 : 69-75%
	{{976, 185}, {2131, 150}},  // Q ==12 : 75-81%
	{{1180, 186}, {2070, 175}}, // Q ==13 : 81-87%
	{{1377, 185}, {1731, 202}}, // Q ==14 : 87-93%
	{{1412, 185}, {1695, 202}}, // Q ==15 : 93-99%
}

// SelectDecoder tells which decoder is likely to decode faster, based on a
// set of pre-computed metrics: 0 for X1, 1 for X2. Assumption: 0 <
// dstSize <= 128 KB. C: huf_decompress.c:HUF_selectDecoder
func SelectDecoder(dstSize, cSrcSize int) uint32 {
	// decoder timing evaluation
	var q uint32 = 15 // Q < 16
	if cSrcSize < dstSize {
		q = uint32(uint64(cSrcSize) * 16 / uint64(dstSize))
	}
	d256 := uint32(dstSize >> 8)
	dTime0 := algoTime[q][0].tableTime + algoTime[q][0].decode256Time*d256
	dTime1 := algoTime[q][1].tableTime + algoTime[q][1].decode256Time*d256
	dTime1 += dTime1 >> 5 // small advantage to algorithm using less memory, to reduce cache eviction
	if dTime1 < dTime0 {
		return 1
	}
	return 0
}

// Decompress1XDCtxWksp decodes a single-stream Huffman literals section
// (table description first) into exactly len(dst) bytes, choosing X1 or
// X2 with SelectDecoder; the table is left in dt for a later repeat.
// C: huf_decompress.c:HUF_decompress1X_DCtx_wksp
func Decompress1XDCtxWksp(dt *DTable, dst, src []byte, flags int) error {
	dstSize, cSrcSize := len(dst), len(src)
	// validation checks
	if dstSize == 0 {
		return ErrDstSizeTooSmall
	}
	if cSrcSize > dstSize {
		return ErrCorruptionDetected // invalid
	}
	if cSrcSize == dstSize { // not compressed
		copy(dst, src)
		return nil
	}
	if cSrcSize == 1 { // RLE
		for i := range dst {
			dst[i] = src[0]
		}
		return nil
	}

	if SelectDecoder(dstSize, cSrcSize) != 0 {
		return decompress1X2DCtxWksp(dt, dst, src, flags)
	}
	return Decompress1X1DCtxWksp(dt, dst, src, flags)
}

// Decompress1XUsingDTable decodes a single stream with the table in dt,
// X1 or X2 according to the table's type (the treeless repeat).
// C: huf_decompress.c:HUF_decompress1X_usingDTable
func Decompress1XUsingDTable(dst, src []byte, dt *DTable, flags int) error {
	if dt.desc.tableType != 0 {
		return Decompress1X2UsingDTable(dst, src, dt, flags)
	}
	return Decompress1X1UsingDTable(dst, src, dt, flags)
}

// Decompress4XUsingDTable decodes four streams with the table in dt, X1 or
// X2 according to the table's type (the treeless repeat). Dispatching on
// the type keeps the X1 loops off a table whose x1 entries are stale.
// C: huf_decompress.c:HUF_decompress4X_usingDTable
func Decompress4XUsingDTable(dst, src []byte, dt *DTable, flags int) error {
	if dt.desc.tableType != 0 {
		return Decompress4X2UsingDTable(dst, src, dt, flags)
	}
	return Decompress4X1UsingDTable(dst, src, dt, flags)
}

// Decompress4XHufOnlyWksp decodes a four-stream Huffman literals section
// (table description first) into exactly len(dst) bytes, choosing X1 or
// X2 with SelectDecoder; the table is left in dt for a later repeat.
// C: huf_decompress.c:HUF_decompress4X_hufOnly_wksp
func Decompress4XHufOnlyWksp(dt *DTable, dst, src []byte, flags int) error {
	// validation checks
	if len(dst) == 0 {
		return ErrDstSizeTooSmall
	}
	if len(src) == 0 {
		return ErrCorruptionDetected
	}

	if SelectDecoder(len(dst), len(src)) != 0 {
		return decompress4X2DCtxWksp(dt, dst, src, flags)
	}
	return decompress4X1DCtxWksp(dt, dst, src, flags)
}

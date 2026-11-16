// Port of libdeflate v1.26 lib/deflate_decompress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the input bitstream parameters (BITBUF_NBITS,
// MAX_BITSLEFT, CONSUMABLE_NBITS, FASTLOOP_PRELOADABLE_NBITS, PRELOAD_SLACK,
// CAN_CONSUME*, FASTLOOP_MAX_BYTES_{WRITTEN,READ}), enum libdeflate_result,
// libdeflate_alloc_decompressor, libdeflate_deflate_decompress{,_ex} and the
// implementation dispatch (decompress_impl / dispatch_decomp).
//
// The decompression routine itself (decompress_template.h, FUNCNAME) is in
// decompress_generic.go.

package deflate

import "errors"

// Input bitstream parameters, for the configuration libdeflate uses on 64-bit
// little-endian CPUs with fast unaligned access (x86-64, arm64): bitbuf_t is
// a 64-bit machine word and UNALIGNED_ACCESS_IS_FAST is 1, so refills are
// branchless word loads (REFILL_BITS_BRANCHLESS). The Go port uses this
// configuration on every GOARCH (bitbuf is always a uint64; on 32-bit targets
// the compiler emulates it), so the result code and the actual in/out
// byte counts are the same as those of the amd64 C build on every platform.
// (C's 32-bit build would allow fewer overread bytes, sizeof(bitbuf_t) == 4,
// and take the CAN_CONSUME == false branches that this port omits.)
// C: deflate_decompress.c, "Input bitstream"
const (
	// C: BITBUF_NBITS (8 * sizeof(bitbuf_t))
	bitbufNbits = 64
	// Size of a machine word in bytes, for match copies. C: WORDBYTES
	wordBytes = 8
	// Maximum number of consumable bits, the maximum of uint8(bitsleft):
	// BITBUF_NBITS - 1 with the branchless refill. C: MAX_BITSLEFT
	maxBitsleft = bitbufNbits - 1
	// Minimum number of consumable bits right after a refill: only whole
	// bytes are added. C: CONSUMABLE_NBITS
	consumableNbits = maxBitsleft - 7
	// Minimum number of preloadable bits right after
	// REFILL_BITS_IN_FASTLOOP (not after REFILL_BITS).
	// C: FASTLOOP_PRELOADABLE_NBITS
	fastloopPreloadableNbits = bitbufNbits
	// Minimum number of bits that are preloadable but not consumable after
	// REFILL_BITS_IN_FASTLOOP and any later consumption. C: PRELOAD_SLACK
	preloadSlack = max(0, fastloopPreloadableNbits-maxBitsleft)

	// Worst-case number of output bytes written by one fast loop
	// iteration: 2 literals, a DEFLATE_MAX_MATCH_LEN match, and the
	// word-copy overrun. C: FASTLOOP_MAX_BYTES_WRITTEN
	//
	// Both fast loop margins are conservative, as in C, and must not be
	// tightened (e.g. to drop bounds checks): the real worst case written
	// is 290 bytes (lit, lit, a 257/258-byte offset-1 match copied in
	// 32-byte rounds = 288), so this margin has 10 bytes of slack, which
	// is deliberate; 11 or 12 bytes less writes past len(dst)
	// (TestFastloopOutputMargin). The 25-byte input margin below is
	// conservative too: with p0 = in_next at the loop test, the one
	// mid-iteration refill (after at most 42 consumed bits) leaves
	// in_next <= p0+6 and the last refill then reads an 8-byte word, so an
	// iteration reads at most up to p0+14 and a margin of 13 would be
	// enough: the margin has 12 bytes of slack. A probe with
	// maximal-input iterations reads past len(src) once the margin is 12
	// (TestFastloopInputMargin). Keep both as C has them.
	fastloopMaxBytesWritten = 2 + deflateMaxMatchLen + (5 * wordBytes) - 1
	// Worst-case number of input bytes read by one fast loop iteration
	// (refill at the start, then 2 fast literals and a match, rounded up to
	// bytes, plus the word REFILL_BITS_BRANCHLESS reads past in_next).
	// C: FASTLOOP_MAX_BYTES_READ
	fastloopMaxBytesRead = (maxBitsleft+(2*litlenTablebits)+lengthMaxbits+offsetMaxbits+7)/8 + bitbufNbits/8
)

// canConsume reports CAN_CONSUME(n): whether n bits can always be consumed
// right after a refill. C: CAN_CONSUME
func canConsume(n int) bool { return consumableNbits >= n }

// canConsumeAndThenPreload reports CAN_CONSUME_AND_THEN_PRELOAD(c, p):
// whether, after REFILL_BITS_IN_FASTLOOP, c bits can always be consumed and
// then p more bits preloaded. C: CAN_CONSUME_AND_THEN_PRELOAD
func canConsumeAndThenPreload(c, p int) bool {
	return consumableNbits >= c && fastloopPreloadableNbits >= c+p
}

// The C code tests CAN_CONSUME / CAN_CONSUME_AND_THEN_PRELOAD with constant
// arguments to select code at compile time. For the 64-bit configuration
// above, these are their values; decompress_generic.go keeps only the branch
// selected, and TestBitstreamConfig checks every one of them with
// canConsume / canConsumeAndThenPreload. A constant that would go negative
// (the condition false) is a compile error.
const (
	// next_block: STATIC_ASSERT(CAN_CONSUME(1 + 2 + 5 + 5 + 4 + 3)).
	_ uint = consumableNbits - (1 + 2 + 5 + 5 + 4 + 3)
	// Precode lens: CAN_CONSUME(3 * (DEFLATE_NUM_PRECODE_SYMS - 1)) is
	// true: the first len is merged with the header fields.
	_ uint = consumableNbits - 3*(deflateNumPrecodeSyms-1)
	// One-literal branch (not taken, but C compiles its assert, template
	// L427): STATIC_ASSERT(CAN_CONSUME_AND_THEN_PRELOAD(LITLEN_TABLEBITS,
	// LITLEN_TABLEBITS)).
	_ uint = consumableNbits - litlenTablebits
	_ uint = fastloopPreloadableNbits - (litlenTablebits + litlenTablebits)
	// Length then offset preload (template L503):
	// STATIC_ASSERT(CAN_CONSUME_AND_THEN_PRELOAD(LENGTH_MAXFASTBITS,
	// OFFSET_TABLEBITS)).
	_ uint = consumableNbits - lengthMaxfastbits
	_ uint = fastloopPreloadableNbits - (lengthMaxfastbits + offsetTablebits)
	// Fast loop literals: both CAN_CONSUME_AND_THEN_PRELOAD tests are
	// true: up to 2 extra fast literals are decoded.
	_ uint = consumableNbits - (2*litlenTablebits + lengthMaxbits)
	_ uint = fastloopPreloadableNbits - (2*litlenTablebits + lengthMaxbits + offsetTablebits)
	_ uint = consumableNbits - (2*litlenTablebits + deflateMaxLitlenCodewordLen)
	_ uint = fastloopPreloadableNbits - (2*litlenTablebits + deflateMaxLitlenCodewordLen + litlenTablebits)
	// Fast loop litlen subtable: no refill needed.
	_ uint = fastloopPreloadableNbits - (deflateMaxLitlenCodewordLen + litlenTablebits)
	_ uint = fastloopPreloadableNbits - (lengthMaxbits + offsetTablebits)
	// Fast loop offset: CAN_CONSUME_AND_THEN_PRELOAD(OFFSET_MAXBITS,
	// LITLEN_TABLEBITS) is true: the 64-bit offset path.
	_ uint = consumableNbits - offsetMaxbits
	_ uint = fastloopPreloadableNbits - (offsetMaxbits + litlenTablebits)
	// Before the match copy: no refill needed before the preload.
	_ uint = fastloopPreloadableNbits - (max(offsetMaxbits-offsetTablebits, offsetMaxfastbits) + litlenTablebits)
	// Generic loop: CAN_CONSUME(LENGTH_MAXBITS + OFFSET_MAXBITS) is true:
	// no refills between the litlen and offset decodes.
	_ uint = consumableNbits - (lengthMaxbits + offsetMaxbits)
)

// Status of decompressFastloopAsm: why the assembly fast loop returned.
const (
	fastloopContinue   = 0 // loop condition false: continue in the Go code
	fastloopEndOfBlock = 1 // end-of-block codeword consumed: C's goto block_done
	fastloopBadData    = 2 // SAFETY_CHECK(offset <= out_next - out) failed
)

// fastloopChunkOut bounds the output of one decompressFastloopAsm call: an
// assembly function is not a preemption point, and a DEFLATE block has no
// size limit (one block can expand to about 1 GiB), so decompressDefault
// (and decompressStream) calls it again every fastloopChunkOut bytes of
// output instead of letting one call hold off a GC stop-the-world for the
// whole block. A call that returns at the lowered output end stops at an
// iteration boundary, where the next call's entry refill and preload are
// idempotent, so the decode is the same.
const fastloopChunkOut = 1 << 20

// fastloopChunk is the bound in use, fastloopChunkOut; only tests lower it
// (to re-enter the assembly at every iteration boundary). It is read once
// per assembly call.
var fastloopChunk = fastloopChunkOut

// Format assumptions of the dynamic block header code, checked in both
// directions (equality). C: decompress_template.h
const (
	// L164: STATIC_ASSERT(PRECODE_TABLEBITS ==
	// DEFLATE_MAX_PRE_CODEWORD_LEN): the precode table has no subtables.
	_ uint = precodeTablebits - deflateMaxPreCodewordLen
	_ uint = deflateMaxPreCodewordLen - precodeTablebits
	// L198: STATIC_ASSERT(DEFLATE_MAX_LENS_OVERRUN == 138 - 1): d.lens has
	// room for the worst repeat overrun (138 zeroes with 1 length left).
	_ uint = deflateMaxLensOverrun - (138 - 1)
	_ uint = (138 - 1) - deflateMaxLensOverrun
)

// libdeflateResult is the result of a decompression call.
// C: enum libdeflate_result (libdeflate.h)
type libdeflateResult int

const (
	// Decompression was successful. C: LIBDEFLATE_SUCCESS
	libdeflateSuccess libdeflateResult = 0
	// Decompression failed because the compressed data was invalid,
	// corrupt, or otherwise unsupported. C: LIBDEFLATE_BAD_DATA
	libdeflateBadData libdeflateResult = 1
	// A NULL actual_out_nbytes_ret was provided, but the data would have
	// decompressed to fewer than out_nbytes_avail bytes.
	// C: LIBDEFLATE_SHORT_OUTPUT
	libdeflateShortOutput libdeflateResult = 2
	// The data would have decompressed to more than out_nbytes_avail
	// bytes. C: LIBDEFLATE_INSUFFICIENT_SPACE
	libdeflateInsufficientSpace libdeflateResult = 3
)

// Errors returned by Decompress and DecompressEx. They map one to one to
// the libdeflate result codes:
//
//	LIBDEFLATE_SUCCESS            -> nil
//	LIBDEFLATE_BAD_DATA           -> ErrBadData
//	LIBDEFLATE_SHORT_OUTPUT       -> ErrShortOutput (Decompress only)
//	LIBDEFLATE_INSUFFICIENT_SPACE -> ErrInsufficientSpace
var (
	ErrBadData           = errors.New("deflate: invalid compressed data")
	ErrShortOutput       = errors.New("deflate: data decompressed to fewer bytes than the output buffer")
	ErrInsufficientSpace = errors.New("deflate: output buffer too small for the decompressed data")
)

// err returns the error value of r (nil for libdeflateSuccess).
func (r libdeflateResult) err() error {
	switch r {
	case libdeflateSuccess:
		return nil
	case libdeflateBadData:
		return ErrBadData
	case libdeflateShortOutput:
		return ErrShortOutput
	default:
		return ErrInsufficientSpace
	}
}

// NewDecompressor returns a new Decompressor. As in C, only
// static_codes_loaded must start out false (C zeroes the whole struct, as
// the Go allocation does). A Decompressor may be reused for any number of
// calls, but not concurrently: each block rebuilds the tables it needs, and
// only consecutive static blocks reuse them (static_codes_loaded). There is
// nothing to free (C: libdeflate_free_decompressor).
// port of libdeflate_alloc_decompressor / libdeflate_alloc_decompressor_ex
func NewDecompressor() *Decompressor {
	return &Decompressor{}
}

// decompressImpl runs the best decompression routine for this CPU. There is
// one routine, the Go port of deflate_decompress_default, called directly as
// C does when the best implementation is statically known (#define
// decompress_impl DEFAULT_IMPL). C's deflate_decompress_bmi2
// (lib/x86/decompress_impl.h) is the same template compiled for BMI2; here
// only its fast loop differs, so decompressDefault itself runs the assembly
// fast loop (decompressFastloopAsm) when useFastloopAsm is set; on arm64 the
// same fast loop runs in assembly too (decompress_fastloop_arm64.s; C has
// no aarch64-specific decompressor).
// C: decompress_impl / dispatch_decomp
func (d *Decompressor) decompressImpl(in, out []byte, wantIn, wantOut bool) (libdeflateResult, int, int) {
	return d.decompressDefault(in, out, wantIn, wantOut)
}

// Decompress decompresses the raw DEFLATE stream at the start of src into
// dst. The stream must decompress to exactly len(dst) bytes: fewer gives
// ErrShortOutput, more ErrInsufficientSpace. Bytes of src after the end of
// the stream are ignored. On success nOut is len(dst); on error it is 0
// and dst holds undefined data. As in libdeflate, dst bytes may be written
// even when an error is returned.
// port of libdeflate_deflate_decompress(d, in, in_nbytes, out,
// out_nbytes_avail, NULL)
func (d *Decompressor) Decompress(dst, src []byte) (nOut int, err error) {
	res, _, _ := d.decompressImpl(src, dst, false, false)
	if res != libdeflateSuccess {
		return 0, res.err()
	}
	return len(dst), nil
}

// DecompressEx decompresses the raw DEFLATE stream at the start of src into
// dst, which may be larger than needed. It returns the number of src bytes
// the stream occupies (nIn; trailing bytes are not counted) and the number of
// bytes written to dst (nOut). ErrShortOutput is never returned. On error nIn
// and nOut are 0 and dst holds undefined data. Bytes of dst past nOut may be
// overwritten (the match copy overruns into the space left; C does the same).
// port of libdeflate_deflate_decompress_ex(d, in, in_nbytes, out,
// out_nbytes_avail, &actual_in_nbytes, &actual_out_nbytes)
func (d *Decompressor) DecompressEx(dst, src []byte) (nIn, nOut int, err error) {
	res, nIn, nOut := d.decompressImpl(src, dst, true, true)
	if res != libdeflateSuccess {
		return 0, 0, res.err()
	}
	return nIn, nOut, nil
}

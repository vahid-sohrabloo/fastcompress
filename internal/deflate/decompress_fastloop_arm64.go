//go:build arm64 && !appengine && !noasm && !purego && gc

package deflate

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useFastloopAsm selects the assembly fast loop of decompressDefault and
// decompressStream (decompress_fastloop_arm64.s: baseline ARMv8.0
// instructions only, so every arm64 CPU runs it). The result is the same as
// the Go fast loop's either way.
//
// The arm64 assembly is checked for equivalence with the Go loop under
// qemu-aarch64 only; its speed has not been measured on arm64 hardware, so
// whether it is faster than the Go loop there is not known yet.
var useFastloopAsm bool

func init() {
	cpu.RegisterAsm("deflate.decompressFastloop", &useFastloopAsm, fastloopAsmAvailable)
}

// fastloopAsmAvailable reports whether the CPU runs the assembly fast loop:
// always on arm64.
func fastloopAsmAvailable() bool { return true }

// The refill thresholds of the offset decode, which the assembly has as
// literals (decompress_fastloop_arm64.s, OFFSET_SUB_REFILL and
// OFFSET_FAST_REFILL); checked equal in both directions.
const (
	_ uint = 38 - (offsetMaxbits + litlenTablebits - preloadSlack)
	_ uint = (offsetMaxbits + litlenTablebits - preloadSlack) - 38
	_ uint = 31 - (offsetMaxfastbits + litlenTablebits - preloadSlack)
	_ uint = (offsetMaxfastbits + litlenTablebits - preloadSlack) - 31
)

// decompressFastloopAsm runs iterations of the fast loop of
// decompressDefault from the state the Go code has right before the loop's
// first REFILL_BITS_IN_FASTLOOP, which must satisfy inNext < inFastloopEnd
// and outNext < outFastloopEnd. It returns the updated state and status:
// fastloopContinue when the loop condition became false (the state is then
// the Go loop's at that point), fastloopEndOfBlock at C's goto block_done
// (the end-of-block codeword consumed), fastloopBadData when C's
// SAFETY_CHECK of a match offset fails. tablebits is d.litlenTablebits.
// C: decompress_template.h fast loop (deflate_decompress_default; C has no
// aarch64-specific decompressor)
//
//go:noescape
func decompressFastloopAsm(in, out []byte, litlenTable *[litlenEnough]uint32, offsetTable *[offsetEnough]uint32,
	tablebits uint32, inFastloopEnd, outFastloopEnd int, bitbuf uint64, bitsleft uint32, inNext, outNext int) (
	bitbufRet uint64, bitsleftRet uint32, inNextRet, outNextRet, status int)

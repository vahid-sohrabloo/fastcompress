//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useFastloopAsm selects the assembly fast loop of decompressDefault and
// decompressStream, which on amd64 needs BMI2 (SHRX/SHLX/BZHI). Without
// BMI2 the Go fast loop runs, as C runs deflate_decompress_default when
// HAVE_BMI2 is false; the result is the same either way.
var useFastloopAsm bool

func init() {
	cpu.RegisterAsm("deflate.decompressFastloopBMI2", &useFastloopAsm, fastloopAsmAvailable)
}

// fastloopAsmAvailable reports whether the CPU runs the assembly fast loop.
func fastloopAsmAvailable() bool { return cpu.X86.HasBMI2 }

// The refill thresholds of the offset decode, which the assembly has as
// literals (decompress_fastloop_amd64.s, OFFSET_SUB_REFILL and
// OFFSET_FAST_REFILL); checked equal in both directions.
const (
	_ uint = 38 - (offsetMaxbits + litlenTablebits - preloadSlack)
	_ uint = (offsetMaxbits + litlenTablebits - preloadSlack) - 38
	_ uint = 31 - (offsetMaxfastbits + litlenTablebits - preloadSlack)
	_ uint = (offsetMaxfastbits + litlenTablebits - preloadSlack) - 31
)

// decompressFastloopAsm runs iterations of the fast loop of
// decompressDefault (C: the fast loop of deflate_decompress_bmi2) from the
// state the Go code has right before the loop's first REFILL_BITS_IN_FASTLOOP,
// which must satisfy inNext < inFastloopEnd and outNext < outFastloopEnd.
// It returns the updated state and status: fastloopContinue when the loop
// condition became false (the state is then the Go loop's at that point),
// fastloopEndOfBlock at C's goto block_done (the end-of-block codeword
// consumed), fastloopBadData when C's SAFETY_CHECK of a match offset fails.
// tablebits is d.litlenTablebits.
// C: decompress_template.h fast loop, compiled as deflate_decompress_bmi2
//
//go:noescape
func decompressFastloopAsm(in, out []byte, litlenTable *[litlenEnough]uint32, offsetTable *[offsetEnough]uint32,
	tablebits uint32, inFastloopEnd, outFastloopEnd int, bitbuf uint64, bitsleft uint32, inNext, outNext int) (
	bitbufRet uint64, bitsleftRet uint32, inNextRet, outNextRet, status int)

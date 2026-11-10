//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import (
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// useWriteSequencesAsm selects writeSequencesAsm for the sequence output of
// flushBlock. It needs only amd64, so it is always available; the switch
// exists for the assembly equivalence tests.
var useWriteSequencesAsm bool

func init() {
	cpu.RegisterAsm("deflate.writeSequences", &useWriteSequencesAsm, func() bool { return true })
}

// The layouts flush_block_amd64.s uses: bitRegs (its field offsets),
// deflateCodes (CW_OFF, LENS, LENS_OFF), deflateLengthCodes (FULL_LENS),
// deflateSequence (8 bytes: litrunlenAndLength, offset, offsetSlot) and the
// constants it has written in.
const (
	_ = uint(unsafe.Offsetof(bitRegs{}.bitcount)-8) + uint(8-unsafe.Offsetof(bitRegs{}.bitcount))
	_ = uint(unsafe.Offsetof(bitRegs{}.next)-16) + uint(16-unsafe.Offsetof(bitRegs{}.next))
	_ = uint(unsafe.Offsetof(bitRegs{}.fastEnd)-24) + uint(24-unsafe.Offsetof(bitRegs{}.fastEnd))
	_ = uint(unsafe.Sizeof(bitRegs{}.bitcount)-8) + uint(8-unsafe.Sizeof(bitRegs{}.bitcount))
	_ = uint(unsafe.Offsetof(deflateCodewords{}.offset)-1152) + uint(1152-unsafe.Offsetof(deflateCodewords{}.offset))
	_ = uint(unsafe.Offsetof(deflateCodes{}.lens)-1280) + uint(1280-unsafe.Offsetof(deflateCodes{}.lens))
	_ = uint(lensOffsetStart-288) + uint(288-lensOffsetStart)
	_ = uint(unsafe.Offsetof(deflateLengthCodes{}.lens)-1036) + uint(1036-unsafe.Offsetof(deflateLengthCodes{}.lens))
	_ = uint(unsafe.Sizeof(deflateSequence{})-8) + uint(8-unsafe.Sizeof(deflateSequence{}))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offset)-4) + uint(4-unsafe.Offsetof(deflateSequence{}.offset))
	_ = uint(unsafe.Offsetof(deflateSequence{}.offsetSlot)-6) + uint(6-unsafe.Offsetof(deflateSequence{}.offsetSlot))
	_ = uint(seqLengthShift-23) + uint(23-seqLengthShift)
	_ = uint(seqLitrunlenMask-0x7FFFFF) + uint(0x7FFFFF-seqLitrunlenMask)
	_ = uint(wordBytes-8) + uint(8-wordBytes)
)

// writeSequencesAsm is writeSequences in assembly: it outputs the literals
// and matches of the sequences starting at seqs (up to and including the one
// with match length 0) with codes and full (codes' full length codewords),
// reading the literals from block, and updates r (bitbuf, bitcount, next;
// fastEnd is read only) like the Go method updates the outputBitstream. Its
// preconditions are listed in the BOUNDS block.
// port of deflate_flush_block (output of the sequences list, with
// WRITE_MATCH)
//
//go:noescape
func writeSequencesAsm(r *bitRegs, out []byte, codes *deflateCodes, full *deflateLengthCodes, block []byte,
	seqs *deflateSequence)

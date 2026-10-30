//go:build arm64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastzstd/internal/cpu"

// useFastAsm selects the assembly main loop of the no-dictionary fast match
// finder (baseline arm64 instructions only).
var useFastAsm bool

func init() {
	cpu.RegisterAsm("fast", &useFastAsm, func() bool { return true })
}

// compressBlockFastAsm runs the search loop of
// compressBlockFastNoDictGeneric from ip0 (C: from _start to _cleanup),
// appending sequences at seqs and literals at lits. It returns the final
// anchor, the number of sequences and literal bytes written, the repcodes,
// and the long-length marker (llType 0 when none; llPos counts from seqs).
// nSeq is -1 if a hash table entry pointed at or after the position being
// searched (impossible: the table only holds earlier positions).
//
// Preconditions (checked by compressBlockFastNoDict): prefixStart <=
// istart <= ip0, ip0 > prefixStart and iend <= len(b); the hash table has
// 1<<hlog entries (hShift = 64 - hlog); lits has room for iend - istart
// bytes and seqs for (iend - istart) / 4 sequences (every match is at least
// 4 bytes); rep1 and rep2 do not reach before prefixStart from ip0 (the Go
// caller's maxRep check).
//
//go:noescape
func compressBlockFastAsm(hashTable *uint32, b *byte, seqs *seqDef, lits *byte,
	ip0, istart, iend, prefixStart int, prefixStartIndex, baseIdx, rep1, rep2 uint32,
	stepSize int, mlsShift, hShift, prime uint64) (anchor, nSeq, nLits int, rep1Out, rep2Out, llType, llPos uint32)

// Register-convention helpers of compressBlockFastAsm (never called from Go).
func fastCopyLits()
func fastCount()

// compressBlockFastNoDict is compressBlockFastNoDictGeneric with the main
// loop in assembly; the setup and the repcode bookkeeping around it are the
// same Go code.
func compressBlockFastNoDict(ms *matchState, ss *seqStore, rep *[repNum]uint32, src []byte, srcIdx uint32, mls uint32) int {
	if !useFastAsm {
		return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	cParams := &ms.cParams
	hlog := cParams.hashLog
	stepSize := int(cParams.targetLength) + 1 // min 2
	if cParams.targetLength == 0 {
		stepSize++
	}
	b := ms.window.base
	baseIdx := ms.window.baseIdx
	endIndex := srcIdx + uint32(len(src))
	prefixStartIndex := getLowestPrefixIndex(ms, endIndex, cParams.windowLog)
	prefixStart := int(prefixStartIndex - baseIdx)
	istart := int(srcIdx - baseIdx)
	iend := istart + len(src)
	nLits, nSeqs := len(ss.literals), len(ss.sequences)
	if len(ms.hashTable) < 1<<hlog || iend > len(b) || prefixStart > istart ||
		cap(ss.literals)-nLits < len(src) || cap(ss.sequences)-nSeqs < len(src)/4 {
		return compressBlockFastNoDictGeneric(ms, ss, rep, src, srcIdx, mls)
	}
	hShift, prime := fastHashParams(mls)

	ip0 := istart
	repOffset1 := rep[0]
	repOffset2 := rep[1]
	var offsetSaved1, offsetSaved2 uint32
	if ip0 == prefixStart {
		ip0++
	}
	{
		curr := uint32(ip0) + baseIdx
		windowLow := getLowestPrefixIndex(ms, curr, cParams.windowLog)
		maxRep := curr - windowLow
		if repOffset2 > maxRep {
			offsetSaved2, repOffset2 = repOffset2, 0
		}
		if repOffset1 > maxRep {
			offsetSaved1, repOffset1 = repOffset1, 0
		}
	}

	seqs := ss.sequences[nSeqs:cap(ss.sequences)]
	lits := ss.literals[nLits:cap(ss.literals)]
	var seqPtr *seqDef
	if len(seqs) > 0 {
		seqPtr = &seqs[0]
	}
	var litPtr *byte
	if len(lits) > 0 {
		litPtr = &lits[0]
	}
	anchor, nSeq, nLit, r1, r2, llType, llPos := compressBlockFastAsm(&ms.hashTable[0], &b[0], seqPtr, litPtr,
		ip0, istart, iend, prefixStart, prefixStartIndex, baseIdx, repOffset1, repOffset2,
		stepSize, uint64(hShift), uint64(64-hlog), prime)
	if nSeq < 0 {
		panic("zstd: fast hash table holds an index at or after the current position")
	}
	ss.sequences = ss.sequences[:nSeqs+nSeq]
	ss.literals = ss.literals[:nLits+nLit]
	if llType != lltNone {
		ss.longLengthType = int(llType)
		ss.longLengthPos = uint32(nSeqs) + llPos
	}
	repOffset1, repOffset2 = r1, r2

	// C: _cleanup — see compressBlockFastNoDictGeneric.
	if offsetSaved1 != 0 && repOffset1 != 0 {
		offsetSaved2 = offsetSaved1
	}
	rep[0] = repOffset1
	if repOffset1 == 0 {
		rep[0] = offsetSaved1
	}
	rep[1] = repOffset2
	if repOffset2 == 0 {
		rep[1] = offsetSaved2
	}
	return iend - anchor
}

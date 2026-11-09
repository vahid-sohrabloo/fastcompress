// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the level 1 compressor, deflate_compress_fastest,
// with its part of the compressor (the 'p.f' member of struct
// libdeflate_compressor) and choose_max_block_end.

package deflate

import (
	"encoding/binary"
	"math/bits"
)

// fastestState is the data for fastest parsing. C allocates it inside the
// compressor's parser union 'p'; the port allocates it only for level 1.
// C: struct libdeflate_compressor p.f
type fastestState struct {
	// Hash table matchfinder. C: ht_mf
	htMf htMatchfinder

	// Matches and literals chosen for the current block (one more for the
	// final literal run). C: sequences[FAST_SEQ_STORE_LENGTH + 1]
	sequences [fastSeqStoreLength + 1]deflateSequence

	// The window base and next_hash (C: the locals in_cur_base and
	// next_hash) at the end of the last deflateCompressFastest call; with
	// htMf, the matchfinder state of its written-out loop, kept for
	// TestCompressFastestHtEquiv. Not C state, never read by the compressor.
	endInCurBase int
	endNextHash  uint32
}

// fastestLoopState is the state fastestLoopAsm reads and updates (the
// locals of deflateCompressFastest's loop; inMaxBlockEnd and niceLen are
// read only).
type fastestLoopState struct {
	inNext        int
	inCurBase     int
	inMaxBlockEnd int
	seq           int
	nextHash      uint32
	niceLen       uint32
}

// chooseMaxBlockEnd returns the index at which the block starting at
// inBlockBegin must end at the latest: inEnd if fewer than softMaxLen +
// MIN_BLOCK_LENGTH bytes remain (so no final block shorter than
// MIN_BLOCK_LENGTH is left over), else inBlockBegin + softMaxLen.
// port of choose_max_block_end
func chooseMaxBlockEnd(inBlockBegin, inEnd, softMaxLen int) int {
	if inEnd-inBlockBegin < softMaxLen+minBlockLength {
		return inEnd
	}
	return inBlockBegin + softMaxLen
}

// deflateCompressFastest is a faster variant of the greedy compressor: it
// uses the ht_matchfinder rather than the hc_matchfinder, and skips the
// block splitting algorithm in favour of blocks ended at
// FAST_SOFT_MAX_BLOCK_LENGTH bytes or FAST_SEQ_STORE_LENGTH matches.
// c.maxSearchDepth has no effect (the depth is HT_MATCHFINDER_BUCKET_SIZE).
// The match chosen at each position is the longest found; there is no lazy
// evaluation. in is not empty.
//
// C's ht_matchfinder_longest_match and ht_matchfinder_skip_bytes are
// forceinline, so C keeps in_cur_base, next_hash and the loop state in
// registers. Go does not inline them (too large), and the call per position
// plus the pointer arguments cost about as much as the search itself, so
// both are written out here statement for statement (the methods of
// ht_matchfinder.go remain the reference, tested against C's matchfinder
// state; TestCompressFastestHtEquiv compares this copy's matchfinder state
// with theirs, and the level 1 differential tests compare its output with
// C's).
// port of deflate_compress_fastest
func deflateCompressFastest(c *Compressor, in []byte, os *outputBitstream) {
	inNext := 0
	inEnd := len(in)
	inCurBase := 0
	maxLen := uint32(deflateMaxMatchLen)
	niceLen := min(c.niceMatchLength, maxLen)
	nextHash := uint32(0)
	pf := c.pf
	mf := &pf.htMf

	mf.init()
	// deviation from C: CompressChunk's history in[:c.histLen] is run
	// through the matchfinder (not output), so matches may reach into it.
	// (Through a copy of the base: taking inCurBase's address would keep it
	// in memory for the whole function.)
	if c.histLen > 0 && inEnd-c.histLen >= htMatchfinderRequiredNbytes {
		nextHash = lzHash(loadLE32(in, 0), htMatchfinderHashOrder)
		histBase := inCurBase
		mf.skipBytes(in, &histBase, 0, inEnd, uint32(c.histLen), &nextHash)
		inCurBase = histBase
	}
	inNext = c.histLen

	for {
		// Starting a new DEFLATE block
		inBlockBegin := inNext
		inMaxBlockEnd := chooseMaxBlockEnd(inNext, inEnd, fastSoftMaxBlockLength)
		seqs := pf.sequences[:]
		seq := 0 // C: seq = c->p.f.sequences

		c.beginSequences(&seqs[seq])

		for {
			remaining := inEnd - inNext

			if useFastestLoopAsm && remaining >= deflateMaxMatchLen {
				// The loop below in assembly, while at least
				// DEFLATE_MAX_MATCH_LEN bytes remain (so maxLen and
				// niceLen are unchanged); it returns at the end of the
				// block or when fewer bytes remain, and the Go loop
				// continues from there.
				st := fastestLoopState{inNext: inNext, inCurBase: inCurBase, inMaxBlockEnd: inMaxBlockEnd,
					seq: seq, nextHash: nextHash, niceLen: niceLen}
				fastestLoopAsm(pf, &c.freqs, in, &st)
				inNext, inCurBase, seq, nextHash = st.inNext, st.inCurBase, st.seq, st.nextHash
				if inNext >= inMaxBlockEnd || seq >= fastSeqStoreLength {
					break
				}
				continue
			}

			if remaining < deflateMaxMatchLen {
				maxLen = uint32(remaining)
				if maxLen < htMatchfinderRequiredNbytes {
					for ; maxLen != 0; maxLen-- {
						c.chooseLiteral(uint32(in[inNext]), &seqs[seq])
						inNext++
					}
					break
				}
				niceLen = min(niceLen, maxLen)
			}

			// C: length = ht_matchfinder_longest_match(...), written out
			// (see htMatchfinder.longestMatch for the comments).
			length := uint32(0)
			bestMatchptr := inNext
			curPos := uint32(inNext - inCurBase)
			if curPos == matchfinderWindowSize {
				mf.slideWindow()
				inCurBase += matchfinderWindowSize
				curPos = 0
			}
			cutoff := mfPos(curPos - matchfinderWindowSize)
			hash := nextHash
			nextHash = lzHash(loadLE32(in, inNext+1), htMatchfinderHashOrder)
			seq4 := loadLE32(in, inNext)
			b := mf.bucket(hash)
			curNode := b[0]
			b[0] = mfPos(curPos)
			if curNode > cutoff {
				matchptr := inCurBase + int(curNode)
				toInsert := curNode
				curNode = b[1]
				b[1] = toInsert
				if loadLE32(in, matchptr) == seq4 {
					// lzExtend(in, inNext, matchptr, 4, maxLen) with its
					// first word compare written out: with maxLen >= 12
					// lzExtend's first step compares the 8 bytes after
					// the 4 matched ones, and most matches end there.
					if maxLen >= 12 {
						if v := loadLE64(in, matchptr+4) ^ loadLE64(in, inNext+4); v != 0 {
							length = 4 + uint32(bits.TrailingZeros64(v)>>3)
						} else {
							length = lzExtend(in, inNext, matchptr, 12, maxLen)
						}
					} else {
						length = lzExtend(in, inNext, matchptr, 4, maxLen)
					}
					bestMatchptr = matchptr
					if curNode > cutoff && length < niceLen {
						matchptr = inCurBase + int(curNode)
						if loadLE32(in, matchptr) == seq4 &&
							loadLE32(in, matchptr+int(length)-3) == loadLE32(in, inNext+int(length)-3) {
							if l := lzExtend(in, inNext, matchptr, 4, maxLen); l > length {
								length = l
								bestMatchptr = matchptr
							}
						}
					}
				} else if curNode > cutoff {
					matchptr = inCurBase + int(curNode)
					if loadLE32(in, matchptr) == seq4 {
						length = lzExtend(in, inNext, matchptr, 4, maxLen)
						bestMatchptr = matchptr
					}
				}
			}

			if length != 0 {
				// Match found
				// C: deflate_choose_match(c, length, offset, false, &seq),
				// written out (chooseMatchStats is too large to inline).
				offset := uint32(inNext - bestMatchptr)
				offsetSlot := deflateGetOffsetSlot(offset)
				c.freqs.litlen[deflateFirstLenSym+uint32(deflateLengthSlot[length])]++
				c.freqs.offset[offsetSlot]++
				s := &seqs[seq]
				s.litrunlenAndLength |= length << seqLengthShift
				s.offset = uint16(offset)
				s.offsetSlot = uint16(offsetSlot)
				seq++
				seqs[seq].litrunlenAndLength = 0

				// C: ht_matchfinder_skip_bytes(in_next + 1, length - 1),
				// written out (see htMatchfinder.skipBytes).
				skipNext := inNext + 1
				if count := length - 1; int(count)+htMatchfinderRequiredNbytes <= inEnd-skipNext {
					pos := int32(skipNext - inCurBase)
					if pos+int32(count)-1 >= matchfinderWindowSize {
						mf.slideWindow()
						inCurBase += matchfinderWindowSize
						pos -= matchfinderWindowSize
					}
					h := nextHash
					// next: the bytes at skipNext+1 .. skipNext+count+3,
					// advanced one byte per step (no bounds checks).
					next := in[skipNext+1 : skipNext+int(count)+4]
					for len(next) >= 4 {
						b := mf.bucket(h)
						b[1] = b[0]
						b[0] = mfPos(pos)
						h = lzHash(binary.LittleEndian.Uint32(next), htMatchfinderHashOrder)
						next = next[1:]
						pos++
					}
					nextHash = h
				}
				inNext += int(length)
			} else {
				// No match found
				c.chooseLiteral(uint32(in[inNext]), &seqs[seq])
				inNext++
			}

			// Check if it's time to output another block.
			if inNext >= inMaxBlockEnd || seq >= fastSeqStoreLength {
				break
			}
		}

		// deviation from C: a CompressChunk with final == false ends no
		// block as final (its last block may then be shorter than
		// MIN_BLOCK_LENGTH, which C only allows for the final block).
		c.finishBlock(os, in[inBlockBegin:inNext], pf.sequences[:], inNext == inEnd && !c.nonFinal)
		if inNext == inEnd || os.overflow {
			break
		}
	}
	pf.endInCurBase, pf.endNextHash = inCurBase, nextHash
}

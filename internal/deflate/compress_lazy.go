// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the lazy compressors of levels 5..7 and 8..9,
// deflate_compress_lazy_generic and its two instantiations
// deflate_compress_lazy and deflate_compress_lazy2.

package deflate

// lazyNextIsBetter reports whether the match (nextLen, nextOffset) found
// 1 (threshold 2) or 2 (threshold 6) positions after the current match
// (curLen, curOffset) is good enough to output literals instead of the
// current match: it must be at least as long, and each extra byte of length
// counts 4 against the difference of the offsets' bit lengths. curOffset
// and, when nextLen >= curLen, nextOffset are not 0.
// C: the conditions `next_len >= cur_len && 4 * (int)(next_len - cur_len) +
// ((int)bsr32(cur_offset) - (int)bsr32(next_offset)) > 2` (and `> 6`) of
// deflate_compress_lazy_generic
func lazyNextIsBetter(curLen, curOffset, nextLen, nextOffset uint32, threshold int) bool {
	return nextLen >= curLen &&
		4*int(nextLen-curLen)+(int(bsr32(curOffset))-int(bsr32(nextOffset))) > threshold
}

// lazyLoopState is the state lazyLoopAsm reads and updates (the locals of
// deflateCompressLazyGeneric's loop; inBlockBegin, inMaxBlockEnd,
// nextRecalcMinLen, minLen, maxSearchDepth and lazy2 are read only).
type lazyLoopState struct {
	inNext           int
	inCurBase        int
	inBlockBegin     int
	inMaxBlockEnd    int
	nextRecalcMinLen int
	seq              int
	nextHash3        uint32
	nextHash4        uint32
	minLen           uint32
	maxLen           uint32
	niceLen          uint32
	maxSearchDepth   uint32
	lazy2            uint32
}

// deflateCompressLazy is the "lazy" DEFLATE compressor (levels 5..7).
// Before choosing a match, it checks to see if there's a better match at
// the next position. If yes, it outputs a literal and continues to the
// next position. If no, it outputs the match.
// port of deflate_compress_lazy
func deflateCompressLazy(c *Compressor, in []byte, os *outputBitstream) {
	deflateCompressLazyGeneric(c, in, os, false)
}

// deflateCompressLazy2 is the "lazy2" compressor (levels 8 and 9). This is
// similar to the regular lazy one, but it looks for a better match at the
// next 2 positions rather than the next 1. This makes it take slightly
// more time, but compress some inputs slightly more.
// port of deflate_compress_lazy2
func deflateCompressLazy2(c *Compressor, in []byte, os *outputBitstream) {
	deflateCompressLazyGeneric(c, in, os, true)
}

// deflateCompressLazyGeneric is the body of the lazy and lazy2 compressors.
// C passes lazy2 as a compile-time constant to an always-inlined function;
// here it is a run-time argument (the function is far too large for Go to
// inline into the wrappers), tested once per match that is not chosen
// immediately: a perfectly predicted branch. in is not empty.
// port of deflate_compress_lazy_generic
func deflateCompressLazyGeneric(c *Compressor, in []byte, os *outputBitstream, lazy2 bool) {
	inNext := 0
	inEnd := len(in)
	inCurBase := 0
	maxLen := uint32(deflateMaxMatchLen)
	niceLen := min(c.niceMatchLength, maxLen)
	maxSearchDepth := c.maxSearchDepth
	var nextHashes [2]uint32
	pg := c.pg
	mf := &pg.hcMf
	stats := &c.splitStats

	mf.init()
	// deviation from C: CompressChunk's history in[:c.histLen] is run
	// through the matchfinder (not output), so matches may reach into it.
	if c.histLen > 0 && inEnd-c.histLen >= 5 {
		seq := loadLE32(in, 0)
		nextHashes[0] = lzHash(seq&0xFFFFFF, hcMatchfinderHash3Order)
		nextHashes[1] = lzHash(seq, hcMatchfinderHash4Order)
		mf.skipBytes(in, &inCurBase, 0, inEnd, uint32(c.histLen), &nextHashes)
	}
	inNext = c.histLen

	for {
		// Starting a new DEFLATE block
		inBlockBegin := inNext
		inMaxBlockEnd := chooseMaxBlockEnd(inNext, inEnd, softMaxBlockLength)
		nextRecalcMinLen := inNext + min(inEnd-inNext, 10000)
		seqs := pg.sequences[:]
		seq := 0 // C: seq = c->p.g.sequences

		stats.init()
		c.beginSequences(&seqs[seq])
		minLen := calculateMinMatchLen(in[inNext:inMaxBlockEnd], maxSearchDepth)
		for {
			if useLazyLoopAsm && inNext < nextRecalcMinLen {
				// The loop below in assembly; it returns at a position
				// where the minimum match length is due for
				// recalculation, or after the first position at which
				// the block may end, and the Go loop continues from
				// there. (inNext < nextRecalcMinLen: it handles at
				// least one position, so the block end check below
				// runs once per position.)
				st := lazyLoopState{inNext: inNext, inCurBase: inCurBase, inBlockBegin: inBlockBegin,
					inMaxBlockEnd: inMaxBlockEnd, nextRecalcMinLen: nextRecalcMinLen, seq: seq,
					nextHash3: nextHashes[0], nextHash4: nextHashes[1], minLen: minLen, maxLen: maxLen,
					niceLen: niceLen, maxSearchDepth: maxSearchDepth, lazy2: b2u(lazy2)}
				lazyLoopAsm(pg, &c.freqs, stats, in, &st)
				inNext, inCurBase, seq = st.inNext, st.inCurBase, st.seq
				nextHashes = [2]uint32{st.nextHash3, st.nextHash4}
				maxLen, niceLen = st.maxLen, st.niceLen
				if inNext >= inMaxBlockEnd || seq >= seqStoreLength ||
					(stats.readyToCheckBlock(inBlockBegin, inNext, inEnd) &&
						stats.doEndBlockCheck(uint32(inNext-inBlockBegin))) {
					break
				}
				continue
			}

			// Recalculate the minimum match length if it hasn't been
			// done recently.
			if inNext >= nextRecalcMinLen {
				minLen = recalculateMinMatchLen(&c.freqs, maxSearchDepth)
				nextRecalcMinLen += min(inEnd-nextRecalcMinLen, inNext-inBlockBegin)
			}

			// Find the longest match at the current position.
			maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, inEnd-inNext)
			curLen, curOffset := mf.search(in, &inCurBase, inNext, minLen-1, maxLen, niceLen,
				maxSearchDepth, &nextHashes)
			if curLen < minLen || (curLen == deflateMinMatchLen && curOffset > 8192) {
				// No match found. Choose a literal.
				c.chooseLiteralStats(uint32(in[inNext]), true, &seqs[seq])
				inNext++
			} else {
				inNext++

				// C: have_cur_match. Each iteration has a match at
				// the current position (inNext - 1); the loop
				// continues where C jumps back to have_cur_match.
				for {
					// If it's very long, choose it immediately.
					if curLen >= niceLen {
						seq = c.chooseMatchStats(curLen, curOffset, true, seqs, seq)
						mf.skipBytes(in, &inCurBase, inNext, inEnd, curLen-1, &nextHashes)
						inNext += int(curLen - 1)
						break
					}

					// Try to find a better match at the next
					// position. Since we already have a match at the
					// current position, only half the
					// max_search_depth is used for the next one.
					maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, inEnd-inNext)
					nextLen, nextOffset := mf.search(in, &inCurBase, inNext, curLen-1, maxLen,
						niceLen, maxSearchDepth>>1, &nextHashes)
					inNext++
					if lazyNextIsBetter(curLen, curOffset, nextLen, nextOffset, 2) {
						// Found a better match at the next position.
						// Output a literal. Then the next match
						// becomes the current match.
						c.chooseLiteralStats(uint32(in[inNext-2]), true, &seqs[seq])
						curLen, curOffset = nextLen, nextOffset
						continue
					}

					if lazy2 {
						// In lazy2 mode, look ahead another position.
						maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, inEnd-inNext)
						nextLen, nextOffset = mf.search(in, &inCurBase, inNext, curLen-1, maxLen,
							niceLen, maxSearchDepth>>2, &nextHashes)
						inNext++
						if lazyNextIsBetter(curLen, curOffset, nextLen, nextOffset, 6) {
							// There's a much better match two
							// positions ahead, so use two literals.
							c.chooseLiteralStats(uint32(in[inNext-3]), true, &seqs[seq])
							c.chooseLiteralStats(uint32(in[inNext-2]), true, &seqs[seq])
							curLen, curOffset = nextLen, nextOffset
							continue
						}
						// No better match at either of the next 2
						// positions. Output the current match.
						seq = c.chooseMatchStats(curLen, curOffset, true, seqs, seq)
						if curLen > 3 {
							mf.skipBytes(in, &inCurBase, inNext, inEnd, curLen-3, &nextHashes)
							inNext += int(curLen - 3)
						}
					} else {
						// No better match at the next position. Output
						// the current match.
						seq = c.chooseMatchStats(curLen, curOffset, true, seqs, seq)
						mf.skipBytes(in, &inCurBase, inNext, inEnd, curLen-2, &nextHashes)
						inNext += int(curLen - 2)
					}
					break
				}
			}

			// Check if it's time to output another block. C:
			// should_end_block, written out so that the per-position
			// ready_to_check_block test is inlined.
			if inNext >= inMaxBlockEnd || seq >= seqStoreLength ||
				(stats.readyToCheckBlock(inBlockBegin, inNext, inEnd) &&
					stats.doEndBlockCheck(uint32(inNext-inBlockBegin))) {
				break
			}
		}

		// deviation from C: a CompressChunk with final == false ends no
		// block as final (its last block may then be shorter than
		// MIN_BLOCK_LENGTH, which C only allows for the final block).
		c.finishBlock(os, in[inBlockBegin:inNext], pg.sequences[:], inNext == inEnd && !c.nonFinal)
		if inNext == inEnd || os.overflow {
			break
		}
	}
}

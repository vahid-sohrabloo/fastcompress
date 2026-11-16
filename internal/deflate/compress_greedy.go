// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the greedy compressor of levels 2..4,
// deflate_compress_greedy, with its part of the compressor (the 'p.g' member
// of struct libdeflate_compressor), adjust_max_and_nice_len and the
// minimum-match-length heuristic of the greedy and lazy parsers
// (choose_min_match_len, calculate_min_match_len,
// recalculate_min_match_len).

package deflate

import "encoding/binary"

// greedyState is the data for greedy or lazy parsing. C allocates it inside
// the compressor's parser union 'p'; the port allocates it only for the
// levels that use it. C: struct libdeflate_compressor p.g
type greedyState struct {
	// Hash chains matchfinder. C: hc_mf
	hcMf hcMatchfinder

	// Matches and literals chosen for the current block (one more for the
	// final literal run). C: sequences[SEQ_STORE_LENGTH + 1]
	sequences [seqStoreLength + 1]deflateSequence
}

// greedyLoopState is the state greedyLoopAsm reads and updates (the locals
// of deflateCompressGreedy's loop; inBlockBegin, inMaxBlockEnd, minLen,
// niceLen and maxSearchDepth are read only).
type greedyLoopState struct {
	inNext         int
	inCurBase      int
	inBlockBegin   int
	inMaxBlockEnd  int
	seq            int
	nextHash3      uint32
	nextHash4      uint32
	minLen         uint32
	niceLen        uint32
	maxSearchDepth uint32
}

// adjustMaxAndNiceLen decreases the maximum and nice match lengths if the
// end of the input buffer (remaining bytes away) is approaching, and
// returns them. port of adjust_max_and_nice_len
func adjustMaxAndNiceLen(maxLen, niceLen uint32, remaining int) (uint32, uint32) {
	if remaining < deflateMaxMatchLen {
		maxLen = uint32(remaining)
		niceLen = min(niceLen, maxLen)
	}
	return maxLen, niceLen
}

// minMatchLens maps the number of used literals to the minimum match
// length; from ARRAY_LEN(min_lens) used literals on it is 3.
// C: min_lens of choose_min_match_len
var minMatchLens = [...]uint8{
	9, 9, 9, 9, 9, 9, 8, 8, 7, 7, 6, 6, 6, 6, 6, 6,
	5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5,
	5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 4, 4, 4,
	4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
	4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
	// The rest is implicitly 3.
}

// C: STATIC_ASSERT(DEFLATE_MIN_MATCH_LEN <= 3) and
// STATIC_ASSERT(ARRAY_LEN(min_lens) <= DEFLATE_NUM_LITERALS + 1)
const (
	_ = uint(3 - deflateMinMatchLen)
	_ = uint(deflateNumLiterals + 1 - len(minMatchLens))
)

// chooseMinMatchLen chooses the minimum match length for the greedy and
// lazy parsers.
//
// By default the minimum match length is 3, the smallest length DEFLATE
// allows. However, with greedy and lazy parsing, some data (e.g. DNA
// sequencing data) benefits greatly from a longer minimum length, typically
// because literals are very cheap. The heuristic makes the minimum match
// length depend on the number of different literals in the data: many
// different literals make literals expensive and short matches worthwhile,
// few make them cheap and short matches not worthwhile.
// port of choose_min_match_len
func chooseMinMatchLen(numUsedLiterals, maxSearchDepth uint32) uint32 {
	if numUsedLiterals >= uint32(len(minMatchLens)) {
		return 3
	}
	minLen := uint32(minMatchLens[numUsedLiterals])
	// With a low max_search_depth, it may be too hard to find long
	// matches.
	if maxSearchDepth < 16 {
		if maxSearchDepth < 5 {
			minLen = min(minLen, 4)
		} else if maxSearchDepth < 10 {
			minLen = min(minLen, 5)
		} else {
			minLen = min(minLen, 7)
		}
	}
	return minLen
}

// calculateMinMatchLen chooses the minimum match length from the literals
// used in (the first 4 KiB of) data.
// port of calculate_min_match_len
func calculateMinMatchLen(data []byte, maxSearchDepth uint32) uint32 {
	// For very short inputs, the static Huffman code has a good chance of
	// being best, in which case there is no reason to avoid short matches.
	if len(data) < 512 {
		return deflateMinMatchLen
	}

	// For an initial approximation, scan the first 4 KiB of data. The
	// caller may use recalculateMinMatchLen to update min_len later.
	var used [256]uint8
	for _, b := range data[:min(len(data), 4096)] {
		used[b] = 1
	}
	numUsedLiterals := uint32(0)
	for _, u := range used {
		numUsedLiterals += uint32(u)
	}
	return chooseMinMatchLen(numUsedLiterals, maxSearchDepth)
}

// recalculateMinMatchLen recalculates the minimum match length for a
// block, now that the distribution of the literals actually used
// (freqs.litlen) is known. port of recalculate_min_match_len
func recalculateMinMatchLen(freqs *deflateFreqs, maxSearchDepth uint32) uint32 {
	literalFreq := uint32(0)
	for _, f := range freqs.litlen[:deflateNumLiterals] {
		literalFreq += f
	}

	cutoff := literalFreq >> 10 // Ignore literals used very rarely.

	numUsedLiterals := uint32(0)
	for _, f := range freqs.litlen[:deflateNumLiterals] {
		if f > cutoff {
			numUsedLiterals++
		}
	}
	return chooseMinMatchLen(numUsedLiterals, maxSearchDepth)
}

// deflateCompressGreedy is the "greedy" DEFLATE compressor: it always
// chooses the longest match found by the hash chains matchfinder (at least
// the block's minimum match length; a length 3 match only at offsets up to
// 4096), and ends blocks at SOFT_MAX_BLOCK_LENGTH bytes, at
// SEQ_STORE_LENGTH matches or where the block splitting heuristic says so.
// in is not empty.
// port of deflate_compress_greedy
func deflateCompressGreedy(c *Compressor, in []byte, os *outputBitstream) {
	inNext := 0
	inEnd := len(in)
	inCurBase := 0
	maxLen := uint32(deflateMaxMatchLen)
	niceLen := min(c.niceMatchLength, maxLen)
	maxSearchDepth := c.maxSearchDepth
	var nextHash3, nextHash4 uint32 // C: next_hashes[0], next_hashes[1]
	pg := c.pg
	mf := &pg.hcMf
	stats := &c.splitStats

	mf.init()
	// deviation from C: CompressChunk's history in[:c.histLen] is run
	// through the matchfinder (not output), so matches may reach into it.
	// (Through copies: taking the address of inCurBase or of the hashes
	// would keep them in memory for the whole function.)
	if c.histLen > 0 && inEnd-c.histLen >= 5 {
		seq := loadLE32(in, 0)
		nextHashes := [2]uint32{lzHash(seq&0xFFFFFF, hcMatchfinderHash3Order), lzHash(seq, hcMatchfinderHash4Order)}
		histBase := inCurBase
		mf.skipBytes(in, &histBase, 0, inEnd, uint32(c.histLen), &nextHashes)
		inCurBase = histBase
		nextHash3, nextHash4 = nextHashes[0], nextHashes[1]
	}
	inNext = c.histLen

	for {
		// Starting a new DEFLATE block
		inBlockBegin := inNext
		inMaxBlockEnd := chooseMaxBlockEnd(inNext, inEnd, softMaxBlockLength)
		seqs := pg.sequences[:]
		seq := 0 // C: seq = c->p.g.sequences

		stats.init()
		c.beginSequences(&seqs[seq])
		minLen := calculateMinMatchLen(in[inNext:inMaxBlockEnd], maxSearchDepth)
		for {
			if useGreedyLoopAsm && inEnd-inNext >= deflateMaxMatchLen {
				// The loop below in assembly, while at least
				// DEFLATE_MAX_MATCH_LEN bytes remain (so maxLen and
				// niceLen are unchanged); it returns after the first
				// position at which the block may end, or when fewer
				// bytes remain, and the Go loop continues from there.
				st := greedyLoopState{inNext: inNext, inCurBase: inCurBase, inBlockBegin: inBlockBegin,
					inMaxBlockEnd: inMaxBlockEnd, seq: seq, nextHash3: nextHash3, nextHash4: nextHash4,
					minLen: minLen, niceLen: niceLen, maxSearchDepth: maxSearchDepth}
				greedyLoopAsm(pg, &c.freqs, stats, in, &st)
				inNext, inCurBase, seq, nextHash3, nextHash4 = st.inNext, st.inCurBase, st.seq, st.nextHash3,
					st.nextHash4
				if inNext >= inMaxBlockEnd || seq >= seqStoreLength ||
					(stats.readyToCheckBlock(inBlockBegin, inNext, inEnd) &&
						stats.doEndBlockCheck(uint32(inNext-inBlockBegin))) {
					break
				}
				continue
			}

			maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, inEnd-inNext)

			// C: length = hc_matchfinder_longest_match(&c->p.g.hc_mf,
			// &in_cur_base, in_next, min_len - 1, max_len, nice_len,
			// c->max_search_depth, next_hashes, &offset), written out
			// statement for statement (hcMatchfinder.longestMatch, which
			// the lazy compressors call, is the reference). C has it
			// forceinline; Go does not inline it, and the call per
			// position with in_cur_base and next_hashes in memory cost
			// 1-7% at levels 2..4 (T8.1). TestHcMatchfinderStateMatchLibdeflate
			// and the greedy differential tests cover this copy.
			var length, offset uint32
		search:
			for {
				if useHCSearchAsm {
					nh := [2]uint32{nextHash3, nextHash4}
					length, offset = hcLongestMatchAsm(mf, in, &inCurBase, inNext, minLen-1, maxLen, niceLen,
						maxSearchDepth, &nh)
					nextHash3, nextHash4 = nh[0], nh[1]
					break search
				}
				bestLen := minLen - 1
				depthRemaining := maxSearchDepth
				bestMatchptr := inNext
				curPos := uint32(inNext - inCurBase)

				if curPos == matchfinderWindowSize {
					mf.slideWindow()
					inCurBase += matchfinderWindowSize
					curPos = 0
				}
				base := inCurBase
				// C: mf_pos_t cutoff = cur_pos - MATCHFINDER_WINDOW_SIZE (truncated to
				// 16 bits).
				cutoff := mfPos(curPos - matchfinderWindowSize)

				if maxLen < 5 { // can we read 4 bytes from 'in_next + 1'?
					length, offset = bestLen, 0
					break search
				}

				// Get the precomputed hash codes.
				hash3 := nextHash3 & hcHash3Mask
				hash4 := nextHash4 & hcHash4Mask

				// From the hash buckets, get the first node of each linked list.
				curNode3 := mf.hash3Tab[hash3]
				curNode4 := mf.hash4Tab[hash4]

				// Update for length 3 matches. This replaces the singleton node in the
				// 'hash3' bucket with the node for the current sequence.
				mf.hash3Tab[hash3] = mfPos(curPos)

				// Update for length 4 matches. This prepends the node for the current
				// sequence to the linked list in the 'hash4' bucket.
				mf.hash4Tab[hash4] = mfPos(curPos)
				mf.nextTab[curPos&hcPosMask] = curNode4

				// Compute the next hash codes.
				nextHashseq := loadLE32(in, inNext+1)
				nextHash3 = lzHash(nextHashseq&0xFFFFFF, hcMatchfinderHash3Order)
				nextHash4 = lzHash(nextHashseq, hcMatchfinderHash4Order)

				var matchptr int
				if bestLen < 4 { // No match of length >= 4 found yet?
					// Check for a length 3 match if needed.
					if curNode3 <= cutoff {
						length, offset = bestLen, uint32(inNext-bestMatchptr)
						break search
					}

					seq4 := loadLE32(in, inNext)

					if bestLen < 3 {
						matchptr = base + int(curNode3)
						if loadLE32(in, matchptr)&0xFFFFFF == seq4&0xFFFFFF {
							bestLen = 3
							bestMatchptr = matchptr
						}
					}

					// Check for a length 4 match.
					if curNode4 <= cutoff {
						length, offset = bestLen, uint32(inNext-bestMatchptr)
						break search
					}

					for {
						// No length 4 match found yet. Check the first 4 bytes.
						matchptr = base + int(curNode4)

						if loadLE32(in, matchptr) == seq4 {
							break
						}

						// The first 4 bytes did not match. Keep trying.
						curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
						depthRemaining--
						if curNode4 <= cutoff || depthRemaining == 0 {
							length, offset = bestLen, uint32(inNext-bestMatchptr)
							break search
						}
					}

					// Found a match of length >= 4. Extend it to its full length.
					bestMatchptr = matchptr
					bestLen = lzExtend(in, inNext, bestMatchptr, 4, maxLen)
					if bestLen >= niceLen {
						length, offset = bestLen, uint32(inNext-bestMatchptr)
						break search
					}
					curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
					depthRemaining--
					if curNode4 <= cutoff || depthRemaining == 0 {
						length, offset = bestLen, uint32(inNext-bestMatchptr)
						break search
					}
				} else if curNode4 <= cutoff || bestLen >= niceLen {
					length, offset = bestLen, uint32(inNext-bestMatchptr)
					break search
				}

				// Check for matches of length >= 5. The 4-byte values at inNext and at
				// inNext+bestLen-3 do not change while bestLen does not; they are loaded
				// once per bestLen (C's compiler hoists them out of the loop).
				seqFirst := loadLE32(in, inNext)
				for {
					seqLast := loadLE32(in, inNext+int(bestLen)-3)
					for {
						matchptr = base + int(curNode4)

						// Already found a length 4 match. Try for a longer match;
						// start by checking the last 4 bytes and the first 4 bytes.
						// (The last byte, the one which would extend the match length
						// by 1, is the most important.)
						if loadLE32(in, matchptr+int(bestLen)-3) == seqLast &&
							loadLE32(in, matchptr) == seqFirst {
							break
						}

						// Continue to the next node in the list.
						curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
						depthRemaining--
						if curNode4 <= cutoff || depthRemaining == 0 {
							length, offset = bestLen, uint32(inNext-bestMatchptr)
							break search
						}
					}

					if l := lzExtend(in, inNext, matchptr, 4, maxLen); l > bestLen {
						// This is the new longest match.
						bestLen = l
						bestMatchptr = matchptr
						if bestLen >= niceLen {
							length, offset = bestLen, uint32(inNext-bestMatchptr)
							break search
						}
					}

					// Continue to the next node in the list.
					curNode4 = mf.nextTab[int(curNode4)&hcPosMask]
					depthRemaining--
					if curNode4 <= cutoff || depthRemaining == 0 {
						length, offset = bestLen, uint32(inNext-bestMatchptr)
						break search
					}
				}
			}

			if length >= minLen && (length > deflateMinMatchLen || offset <= 4096) {
				// Match found
				seq = c.chooseMatchStats(length, offset, true, seqs, seq)

				// C: hc_matchfinder_skip_bytes(&c->p.g.hc_mf, &in_cur_base,
				// in_next + 1, in_end, length - 1, next_hashes), written out
				// (see hcMatchfinder.skipBytes).
				skipNext := inNext + 1
				if count := length - 1; int(count)+5 <= inEnd-skipNext {
					curPos := uint32(skipNext - inCurBase)
					next := in[skipNext+1 : skipNext+int(count)+4]
					for len(next) >= 4 {
						if curPos == matchfinderWindowSize {
							mf.slideWindow()
							inCurBase += matchfinderWindowSize
							curPos = 0
						}
						mf.hash3Tab[nextHash3&hcHash3Mask] = mfPos(curPos)
						mf.nextTab[curPos&hcPosMask] = mf.hash4Tab[nextHash4&hcHash4Mask]
						mf.hash4Tab[nextHash4&hcHash4Mask] = mfPos(curPos)

						nextHashseq := binary.LittleEndian.Uint32(next)
						next = next[1:]
						nextHash3 = lzHash(nextHashseq&0xFFFFFF, hcMatchfinderHash3Order)
						nextHash4 = lzHash(nextHashseq, hcMatchfinderHash4Order)
						curPos++
					}
				}
				inNext += int(length)
			} else {
				// No match found
				c.chooseLiteralStats(uint32(in[inNext]), true, &seqs[seq])
				inNext++
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

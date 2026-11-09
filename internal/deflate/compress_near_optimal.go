// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the near-optimal compressor of levels 10..12,
// deflate_compress_near_optimal, with its part of the compressor (the 'p.n'
// member of struct libdeflate_compressor, incl. the match cache and the
// optimum nodes), struct deflate_optimum_node, OPTIMUM_OFFSET_SHIFT,
// OPTIMUM_LEN_MASK, deflate_tally_item_list, deflate_choose_all_literals,
// deflate_find_min_cost_path, deflate_optimize_and_flush_block and
// deflate_near_optimal_{init,merge,save,clear_old}_stats. The cost model is
// in costs.go, the binary trees matchfinder in bt_matchfinder.go; the block
// output from the optimum nodes is part of flushBlock (flush_block.go).

package deflate

// The match/literal representation of deflateOptimumNode.item: the low
// bits are the length (1 for a literal, else the match length), the high
// bits the literal byte or the match offset. C: OPTIMUM_OFFSET_SHIFT,
// OPTIMUM_LEN_MASK
const (
	optimumOffsetShift = 9
	optimumLenMask     = (uint32(1) << optimumOffsetShift) - 1
)

// deflateOptimumNode is a byte position of the block and a node in the
// graph of possible match/literal choices: costToEnd is the minimum cost to
// reach the end of the block from this position, item the literal or match
// that starts that minimum-cost path. C: struct deflate_optimum_node
type deflateOptimumNode struct {
	costToEnd uint32 // C: cost_to_end
	item      uint32 // C: item
}

// nearOptimalState is the data for near-optimal parsing. C allocates it
// inside the compressor's parser union 'p'; the port allocates it only for
// levels 10..12 (it is about 9 MiB). The parameters max_optim_passes,
// min_improvement_to_continue, min_bits_to_use_nonfinal_path and
// max_len_to_optimize_static_block are compressorState.pn.
// C: struct libdeflate_compressor p.n
type nearOptimalState struct {
	// Binary tree matchfinder. C: bt_mf
	btMf btMatchfinder

	// Cached matches for the current block: for each position, the
	// matches found there (sorted by strictly increasing length), then a
	// header whose length is the number of those matches and whose offset
	// is the literal at the position. In rare cases the array would
	// overflow; the block is ended once MATCH_CACHE_LENGTH entries are
	// used, and the extra entries absorb the worst case (from entry
	// MATCH_CACHE_LENGTH - 1: MAX_MATCHES_PER_POS matches and a header,
	// then the headers of DEFLATE_MAX_MATCH_LEN - 1 skipped positions).
	// C: match_cache
	matchCache [matchCacheLength + maxMatchesPerPos + deflateMaxMatchLen - 1]lzMatch

	// One node per position for the minimum-cost path algorithm: the
	// worst case MAX_BLOCK_LENGTH plus the end-of-block node.
	// C: optimum_nodes
	optimumNodes [maxBlockLength + 1]deflateOptimumNode

	// The current cost model, and a saved one. C: costs, costs_saved
	costs      deflateCosts
	costsSaved deflateCosts

	// Offset => offset slot, the full map for offsets 1..32768
	// (Compressor.offsetSlotFull points here). C: offset_slot_full
	offsetSlotFull [deflateMaxMatchOffset + 1]uint8

	// Literal/match statistics saved from the previous block.
	// C: prev_observations, prev_num_observations
	prevObservations    [numObservationTypes]uint32
	prevNumObservations uint32

	// Approximate match length frequencies based on a greedy parse,
	// gathered during matchfinding, for setting the initial symbol costs.
	// C: new_match_len_freqs, match_len_freqs
	newMatchLenFreqs [deflateMaxMatchLen + 1]uint32
	matchLenFreqs    [deflateMaxMatchLen + 1]uint32

	// The one sequence of the "only literals" strategy's block (C: the
	// local seq_ of deflate_optimize_and_flush_block; kept here so that it
	// does not escape to the heap per block).
	onlyLitsSeq [1]deflateSequence
}

// tallyItemList follows the minimum-cost path from optimum node 0 to the
// end of the block and counts the Huffman symbols it needs (and the
// end-of-block symbol) in c.freqs. port of deflate_tally_item_list
func (c *Compressor) tallyItemList(blockLength uint32) {
	ns := c.pnState
	nodes := &ns.optimumNodes
	slotFull := c.offsetSlotFull

	for cur := uint32(0); cur != blockLength; {
		item := nodes[cur].item
		length := item & optimumLenMask
		offset := item >> optimumOffsetShift

		if length == 1 {
			// Literal
			c.freqs.litlen[offset&0xFF]++
		} else {
			// Match
			c.freqs.litlen[deflateFirstLenSym+uint32(deflateLengthSlot[length])]++
			c.freqs.offset[slotFull[offset]&0x1F]++
		}
		cur += length
	}

	// Tally the end-of-block symbol.
	c.freqs.litlen[deflateEndOfBlock]++
}

// chooseAllLiterals sets c.freqs and c.codes for the block output as
// literals only. port of deflate_choose_all_literals
func (c *Compressor) chooseAllLiterals(block []byte) {
	c.resetSymbolFrequencies()
	for _, b := range block {
		c.freqs.litlen[b]++
	}
	c.freqs.litlen[deflateEndOfBlock]++

	deflateMakeHuffmanCodes(&c.freqs, &c.codes)
}

// findMinCostPath finds the minimum-cost path through the graph of
// possible match/literal choices of the block, from optimum node 0 to node
// blockLength, with the edge costs of c.pnState.costs; cacheEnd is the
// index of the match cache entry just after the block's last position. The
// path is walked backwards one node at a time; at each node the minimum
// cost to the end and the first literal or match of that path are saved.
// Then c.freqs and c.codes are set to match the path.
// port of deflate_find_min_cost_path
func (c *Compressor) findMinCostPath(blockLength uint32, cacheEnd int) {
	ns := c.pnState
	nodes := ns.optimumNodes[:]
	cache := ns.matchCache[:]
	costs := &ns.costs
	slotFull := c.offsetSlotFull
	cur := int(blockLength)
	cp := cacheEnd

	nodes[cur].costToEnd = 0
	for {
		cur--
		cp--

		numMatches := int(cache[cp].length)
		literal := uint32(cache[cp].offset)

		// It's always possible to choose a literal.
		bestCostToEnd := costs.literal[literal&0xFF] + nodes[cur+1].costToEnd
		item := (literal << optimumOffsetShift) | 1

		// Also consider matches if there are any.
		if numMatches != 0 {
			// Consider each length from the minimum
			// (DEFLATE_MIN_MATCH_LEN) to the length of the longest match
			// found at this position. For each length, consider only the
			// smallest offset for which that length is available (not
			// guaranteed to be optimal, as a larger offset can cost less
			// to code, but a very useful heuristic).
			// (C's cur_node + len is here[ln].)
			here := nodes[cur:]
			ln := uint32(deflateMinMatchLen)
			for m := cp - numMatches; m != cp; m++ {
				match := cache[m]
				offset := uint32(match.offset)
				offsetCost := costs.offsetSlot[slotFull[offset]&0x1F]
				matchLen := uint32(match.length)
				offsetItem := offset << optimumOffsetShift
				for {
					costToEnd := offsetCost + costs.length[ln] + here[ln].costToEnd
					if costToEnd < bestCostToEnd {
						bestCostToEnd = costToEnd
						item = offsetItem + ln // = ln | offsetItem (ln < 512)
					}
					ln++
					if ln > matchLen {
						break
					}
				}
			}
			cp -= numMatches
		}
		nodes[cur].costToEnd = bestCostToEnd
		nodes[cur].item = item
		if cur == 0 {
			break
		}
	}

	c.resetSymbolFrequencies()
	c.tallyItemList(blockLength)
	deflateMakeHuffmanCodes(&c.freqs, &c.codes)
}

// optimizeAndFlushBlock chooses the literals and matches of the block (the
// minimum-cost path through its graph of choices under a cost model) and
// outputs it. The true cost of each symbol is unknown until the Huffman
// codes are built, which depend on the chosen symbols, so several passes
// are made: the first with default costs (mixed with the previous block's
// costs when that seems appropriate), each later one with the costs of the
// previous pass's codes. Using only literals, and (for small blocks) the
// optimal path for the static codes, are considered as alternatives.
// cacheEnd is the match cache index just after the block's last position.
// It returns whether the only-literals strategy was used.
// port of deflate_optimize_and_flush_block
func (c *Compressor) optimizeAndFlushBlock(os *outputBitstream, block []byte, cacheEnd int,
	isFirstBlock, isFinalBlock bool) (usedOnlyLiterals bool) {
	ns := c.pnState
	blockLength := uint32(len(block))
	numPassesRemaining := c.pn.maxOptimPasses
	bestTrueCost := ^uint32(0)
	var trueCost uint32
	staticCost := ^uint32(0)
	var seq []deflateSequence

	// On some data, using only literals (no matches) ends up being better
	// than what the iterative optimization algorithm produces.
	c.chooseAllLiterals(block)
	onlyLitsCost := c.computeTrueCost()

	// Force the block to really end at the desired length, even if some
	// matches extend beyond it.
	for i := blockLength; i <= min(blockLength-1+deflateMaxMatchLen, uint32(len(ns.optimumNodes)-1)); i++ {
		ns.optimumNodes[i].costToEnd = 0x80000000
	}

	// Sometimes a static Huffman block ends up being cheapest, particularly
	// if the block is small. So, if the block is sufficiently small, find
	// the optimal static block solution and remember its cost.
	if blockLength <= c.pn.maxLenToOptimizeStaticBlock {
		// Save the costs temporarily.
		ns.costsSaved = ns.costs

		c.setCostsFromCodes(&c.staticCodes.lens)
		c.findMinCostPath(blockLength, cacheEnd)
		staticCost = ns.optimumNodes[0].costToEnd / bitCost
		staticCost += 7 // for the end-of-block symbol

		// Restore the costs.
		ns.costs = ns.costsSaved
	}

	// Initialize the costs with default costs.
	c.setInitialCosts(block, isFirstBlock)

	for {
		// Find the minimum-cost path for this pass; this also sets
		// c.freqs and c.codes to match the path.
		c.findMinCostPath(blockLength, cacheEnd)

		// Compute the exact cost of the block if the path were used (it
		// uses the actual Huffman codes, not the cost model).
		trueCost = c.computeTrueCost()

		// If the cost didn't improve much from the previous pass, then
		// doing more passes probably won't be helpful, so stop early.
		if trueCost+c.pn.minImprovementToContinue > bestTrueCost {
			break
		}

		bestTrueCost = trueCost

		// Save the cost model that gave bestTrueCost.
		ns.costsSaved = ns.costs

		// Update the cost model from the Huffman codes.
		c.setCostsFromCodes(&c.codes.lens)

		numPassesRemaining--
		if numPassesRemaining == 0 {
			break
		}
	}

	if min(onlyLitsCost, staticCost) < bestTrueCost {
		if onlyLitsCost < staticCost {
			// Using only literals ended up being best!
			c.chooseAllLiterals(block)
			c.setCostsFromCodes(&c.codes.lens)
			ns.onlyLitsSeq[0].litrunlenAndLength = blockLength
			seq = ns.onlyLitsSeq[:]
			usedOnlyLiterals = true
		} else {
			// Static block ended up being best!
			c.setCostsFromCodes(&c.staticCodes.lens)
			c.findMinCostPath(blockLength, cacheEnd)
		}
	} else if trueCost >= bestTrueCost+c.pn.minBitsToUseNonfinalPath {
		// The best solution was actually from a non-final optimization
		// pass, so recover and use the min-cost path from that pass.
		ns.costs = ns.costsSaved
		c.findMinCostPath(blockLength, cacheEnd)
		c.setCostsFromCodes(&c.codes.lens)
	}
	c.flushBlock(os, block, seq, isFinalBlock)
	return usedOnlyLiterals
}

// nearOptimalInitStats starts a block's statistics from scratch.
// port of deflate_near_optimal_init_stats
func (c *Compressor) nearOptimalInitStats() {
	c.splitStats.init()
	clear(c.pnState.newMatchLenFreqs[:])
	clear(c.pnState.matchLenFreqs[:])
}

// nearOptimalMergeStats merges the new observations and match length
// frequencies into the block's. port of deflate_near_optimal_merge_stats
func (c *Compressor) nearOptimalMergeStats() {
	ns := c.pnState
	c.splitStats.mergeNewObservations()
	for i := range ns.matchLenFreqs {
		ns.matchLenFreqs[i] += ns.newMatchLenFreqs[i]
		ns.newMatchLenFreqs[i] = 0
	}
}

// nearOptimalSaveStats saves the literal/match statistics of the block
// just flushed, so that adjustCosts can decide how much the next block
// differs from it. port of deflate_near_optimal_save_stats
func (c *Compressor) nearOptimalSaveStats() {
	ns := c.pnState
	ns.prevObservations = c.splitStats.observations
	ns.prevNumObservations = c.splitStats.numObservations
}

// nearOptimalClearOldStats clears the statistics of the block just
// flushed, leaving those of the beginning of the next block (the new
// observations and match length frequencies).
// port of deflate_near_optimal_clear_old_stats
func (c *Compressor) nearOptimalClearOldStats() {
	c.splitStats.observations = [numObservationTypes]uint32{}
	c.splitStats.numObservations = 0
	clear(c.pnState.matchLenFreqs[:])
}

// deflateCompressNearOptimal is the "near-optimal" DEFLATE compressor of
// levels 10..12. It caches the matches the binary trees matchfinder finds
// at (almost) every position of a block, then computes the block's
// representation as a minimum-cost path over the graph of match/literal
// choices with an iteratively refined cost model (optimizeAndFlushBlock).
// The result is not guaranteed optimal (block boundaries are chosen
// heuristically, not all matches are considered, and symbol costs are only
// known once the symbols are chosen). in is not empty.
// port of deflate_compress_near_optimal
func deflateCompressNearOptimal(c *Compressor, in []byte, os *outputBitstream) {
	ns := c.pnState
	inNext := 0
	inBlockBegin := inNext
	inEnd := len(in)
	inCurBase := inNext
	inNextSlide := inNext + min(inEnd-inNext, matchfinderWindowSize)
	maxLen := uint32(deflateMaxMatchLen)
	niceLen := min(c.niceMatchLength, maxLen)
	maxSearchDepth := c.maxSearchDepth
	cache := ns.matchCache[:]
	cachePtr := 0
	var nextHashes [2]uint32
	prevBlockUsedOnlyLiterals := false
	mf := &ns.btMf
	stats := &c.splitStats

	mf.init()
	// deviation from C: CompressChunk's history in[:c.histLen] is run
	// through the matchfinder (not output), so matches may reach into it.
	// The window is slid from position 0 as if C had parsed the history.
	if c.histLen > 0 {
		seq := loadLE32(in, 0)
		nextHashes[0] = lzHash(seq&0xFFFFFF, btMatchfinderHash3Order)
		nextHashes[1] = lzHash(seq, btMatchfinderHash4Order)
		for p := 0; p < c.histLen; p++ {
			maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, inEnd-p)
			if maxLen >= btMatchfinderRequiredNbytes {
				mf.skipByte(in, inCurBase, p-inCurBase, niceLen, maxSearchDepth, &nextHashes)
			}
		}
		inNext = c.histLen
		inBlockBegin = inNext
	}
	c.nearOptimalInitStats()

	for {
		// Starting a new DEFLATE block
		inMaxBlockEnd := chooseMaxBlockEnd(inBlockBegin, inEnd, softMaxBlockLength)
		prevEndBlockCheck := -1 // C: NULL
		changeDetected := false
		nextObservation := inNext
		var minLen uint32

		// Use the minimum match length heuristic to improve the
		// literal/match statistics gathered during matchfinding (the
		// near-optimal parse itself does not respect min_len). If only
		// literals were best for the previous block, the heuristic is
		// probably still not aggressive enough: gather literal stats only.
		if prevBlockUsedOnlyLiterals {
			minLen = deflateMaxMatchLen + 1
		} else {
			minLen = calculateMinMatchLen(in[inBlockBegin:inMaxBlockEnd], maxSearchDepth)
		}

		// Find matches until we decide to end the block: (1) maximum block
		// length reached, (2) the match cache may overflow, (3) the block
		// split heuristic says to split now.
		for {
			remaining := inEnd - inNext

			// Slide the window forward if needed.
			if inNext == inNextSlide {
				mf.slideWindow()
				inCurBase = inNext
				inNextSlide = inNext + min(remaining, matchfinderWindowSize)
			}

			// Find matches with the current position using the binary tree
			// matchfinder and save them in the match cache.
			matches := cachePtr
			bestLen := uint32(0)
			maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, remaining)
			if maxLen >= btMatchfinderRequiredNbytes {
				cachePtr += mf.getMatches(in, inCurBase, inNext-inCurBase, maxLen, niceLen, maxSearchDepth,
					&nextHashes, (*[maxMatchesPerPos]lzMatch)(cache[cachePtr:]))
				if cachePtr > matches {
					bestLen = uint32(cache[cachePtr-1].length)
				}
			}
			if inNext >= nextObservation {
				if bestLen >= minLen {
					stats.observeMatch(bestLen)
					nextObservation = inNext + int(bestLen)
					ns.newMatchLenFreqs[bestLen]++
				} else {
					stats.observeLiteral(in[inNext])
					nextObservation = inNext + 1
				}
			}

			cache[cachePtr] = lzMatch{uint16(cachePtr - matches), uint16(in[inNext])}
			inNext++
			cachePtr++

			// If there was a very long match found, don't cache any matches
			// for the bytes covered by that match. This avoids degenerate
			// behavior when compressing highly redundant data, where the
			// number of matches can be very large (it doesn't hurt the
			// ratio much: such data must be highly compressible anyway).
			if bestLen >= deflateMinMatchLen && bestLen >= niceLen {
				bestLen--
				for {
					remaining = inEnd - inNext
					if inNext == inNextSlide {
						mf.slideWindow()
						inCurBase = inNext
						inNextSlide = inNext + min(remaining, matchfinderWindowSize)
					}
					maxLen, niceLen = adjustMaxAndNiceLen(maxLen, niceLen, remaining)
					if maxLen >= btMatchfinderRequiredNbytes {
						mf.skipByte(in, inCurBase, inNext-inCurBase, niceLen, maxSearchDepth, &nextHashes)
					}
					cache[cachePtr] = lzMatch{0, uint16(in[inNext])}
					inNext++
					cachePtr++
					bestLen--
					if bestLen == 0 {
						break
					}
				}
			}
			// Maximum block length or end of input reached?
			if inNext >= inMaxBlockEnd {
				break
			}
			// Match cache overflowed?
			if cachePtr >= matchCacheLength {
				break
			}
			// Not ready to try to end the block (again)?
			if !stats.readyToCheckBlock(inBlockBegin, inNext, inEnd) {
				continue
			}
			// Check if it would be worthwhile to end the block.
			if stats.doEndBlockCheck(uint32(inNext - inBlockBegin)) {
				changeDetected = true
				break
			}
			// Ending the block doesn't seem worthwhile here.
			c.nearOptimalMergeStats()
			prevEndBlockCheck = inNext
		}

		// All the matches for this block have been cached. Now choose the
		// precise end of the block and the sequence of items to output to
		// represent it, then flush the block.
		if changeDetected && prevEndBlockCheck >= 0 {
			// The block is being ended because a recent chunk of data
			// differs from the rest of the block. Rather than include the
			// differing chunk (as the greedy and lazy compressors do),
			// rewind to just before it and output a block that only goes
			// up to there; the next block starts with the work already done
			// on it (matches found, stats gathered).
			origCachePtr := cachePtr
			inBlockEnd := prevEndBlockCheck
			numBytesToRewind := inNext - inBlockEnd

			// Rewind the match cache.
			for {
				cachePtr--
				cachePtr -= int(cache[cachePtr].length)
				numBytesToRewind--
				if numBytesToRewind == 0 {
					break
				}
			}
			cacheLenRewound := origCachePtr - cachePtr

			// deviation from C: a chunk's first block is a first block
			// (costs are not carried over from an earlier call).
			prevBlockUsedOnlyLiterals = c.optimizeAndFlushBlock(os, in[inBlockBegin:inBlockEnd], cachePtr,
				inBlockBegin == c.histLen, false)
			copy(cache, cache[cachePtr:origCachePtr])
			cachePtr = cacheLenRewound
			c.nearOptimalSaveStats()
			// Clear the stats for the just-flushed block, leaving just the
			// stats for the beginning of the next block.
			c.nearOptimalClearOldStats()
			inBlockBegin = inBlockEnd
		} else {
			// The block is being ended for a reason other than a differing
			// data chunk being detected: end it at the current position.
			c.nearOptimalMergeStats()
			// deviation from C: a chunk's first block is a first block
			// (costs are not carried over from an earlier call), and a
			// CompressChunk with final == false ends no block as final
			// (its last block may then be shorter than MIN_BLOCK_LENGTH).
			prevBlockUsedOnlyLiterals = c.optimizeAndFlushBlock(os, in[inBlockBegin:inNext], cachePtr,
				inBlockBegin == c.histLen, inNext == inEnd && !c.nonFinal)
			cachePtr = 0
			c.nearOptimalSaveStats()
			c.nearOptimalInitStats()
			inBlockBegin = inNext
		}
		if inNext == inEnd || os.overflow {
			break
		}
	}
}

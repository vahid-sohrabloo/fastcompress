// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the block splitting algorithm (struct
// block_split_stats, NUM_LITERAL_OBSERVATION_TYPES,
// NUM_MATCH_OBSERVATION_TYPES, NUM_OBSERVATION_TYPES,
// NUM_OBSERVATIONS_PER_BLOCK_CHECK, init_block_split_stats, observe_literal,
// observe_match, merge_new_observations, do_end_block_check,
// ready_to_check_block and should_end_block).
//
// The problem is to decide when it is worthwhile to start a new block with
// new Huffman codes. As an approximation of the (far too slow) optimal
// solution, symbols are counted and after every N symbols the expected
// distribution of symbols based on the previous data is compared with the
// actual distribution; if they differ "by enough" (the sum of absolute
// differences exceeds a constant, plus a number proportional to the block
// length), a new block is started. Symbols are aggregated into a few
// "observation types": for literals the high and low bits, for matches
// whether the match is long. The parsers feed it their preliminary (greedy)
// choices.
//
// All arithmetic is C's u32 arithmetic (wrapping), except the one u64
// product in do_end_block_check.

package deflate

// Block split statistics. C: NUM_LITERAL_OBSERVATION_TYPES,
// NUM_MATCH_OBSERVATION_TYPES, NUM_OBSERVATION_TYPES,
// NUM_OBSERVATIONS_PER_BLOCK_CHECK
const (
	numLiteralObservationTypes   = 8
	numMatchObservationTypes     = 2
	numObservationTypes          = numLiteralObservationTypes + numMatchObservationTypes
	numObservationsPerBlockCheck = 512
)

// blockSplitStats holds the block split statistics of the current block.
// C: struct block_split_stats
type blockSplitStats struct {
	newObservations    [numObservationTypes]uint32 // C: new_observations
	observations       [numObservationTypes]uint32 // C: observations
	numNewObservations uint32                      // C: num_new_observations
	numObservations    uint32                      // C: num_observations
}

// init initializes the block split statistics when starting a new block.
// port of init_block_split_stats
func (s *blockSplitStats) init() {
	*s = blockSplitStats{}
}

// observeLiteral records a literal observation. Heuristic: use the top 2
// bits and the low 1 bit of the literal, for 8 possible literal
// observation types. port of observe_literal
func (s *blockSplitStats) observeLiteral(lit uint8) {
	s.newObservations[((lit>>5)&0x6)|(lit&1)]++
	s.numNewObservations++
}

// observeMatch records a match observation. Heuristic: one observation
// type for "short match" and one for "long match" (length >= 9).
// port of observe_match
func (s *blockSplitStats) observeMatch(length uint32) {
	s.newObservations[numLiteralObservationTypes+b2u(length >= 9)]++
	s.numNewObservations++
}

// mergeNewObservations adds the new observations to the block's
// observations and clears them. port of merge_new_observations
func (s *blockSplitStats) mergeNewObservations() {
	for i := range numObservationTypes {
		s.observations[i] += s.newObservations[i]
		s.newObservations[i] = 0
	}
	s.numObservations += s.numNewObservations
	s.numNewObservations = 0
}

// doEndBlockCheck reports whether the distribution of the new observations
// differs enough from that of the block's observations so far (the block
// being blockLength bytes long) to end the block; if not, the new
// observations are merged into the block's. port of do_end_block_check
func (s *blockSplitStats) doEndBlockCheck(blockLength uint32) bool {
	if s.numObservations > 0 {
		// Compute the sum of absolute differences of probabilities. To
		// avoid floating point arithmetic and slow divisions, every
		// probability is multiplied by num_observations *
		// num_new_observations: e.g. the "old" probability
		// observations[i] / num_observations becomes observations[i] *
		// num_new_observations.
		totalDelta := uint32(0)
		for i := range numObservationTypes {
			expected := s.observations[i] * s.numNewObservations
			actual := s.newObservations[i] * s.numObservations
			delta := expected - actual
			if actual > expected {
				delta = actual - expected
			}
			totalDelta += delta
		}

		numItems := s.numObservations + s.numNewObservations

		// Heuristic: the cutoff is when the sum of absolute differences
		// of probabilities becomes at least 200/512 (scaled as above;
		// careful to avoid integer overflow).
		cutoff := s.numNewObservations * 200 / 512 * s.numObservations

		// Very short blocks have a lot of overhead for the Huffman codes,
		// so only use them if it clearly seems worthwhile. (This is an
		// additional penalty, which adds to the smaller penalty below
		// which scales more slowly.) C: a u64 product, truncated back to
		// u32 by the += .
		if blockLength < 10000 && numItems < 8192 {
			cutoff += uint32(uint64(cutoff) * uint64(8192-numItems) / 8192)
		}

		// Ready to end the block?
		if totalDelta+(blockLength/4096)*s.numObservations >= cutoff {
			return true
		}
	}
	s.mergeNewObservations()
	return false
}

// readyToCheckBlock reports whether enough new observations exist, and the
// block (inBlockBegin..inNext) and the rest of the input (inNext..inEnd)
// are long enough, to consider ending the block at inNext.
// port of ready_to_check_block
func (s *blockSplitStats) readyToCheckBlock(inBlockBegin, inNext, inEnd int) bool {
	return s.numNewObservations >= numObservationsPerBlockCheck &&
		inNext-inBlockBegin >= minBlockLength &&
		inEnd-inNext >= minBlockLength
}

// shouldEndBlock reports whether the block starting at inBlockBegin should
// end at inNext (input ends at inEnd). port of should_end_block
func (s *blockSplitStats) shouldEndBlock(inBlockBegin, inNext, inEnd int) bool {
	// Ready to try to end the block (again)?
	if !s.readyToCheckBlock(inBlockBegin, inNext, inEnd) {
		return false
	}
	return s.doEndBlockCheck(uint32(inNext - inBlockBegin))
}

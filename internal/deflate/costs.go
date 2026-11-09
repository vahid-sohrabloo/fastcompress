// Port of libdeflate v1.26 lib/deflate_compress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the cost model of the near-optimal parser
// (struct deflate_costs, deflate_compute_true_cost,
// deflate_set_costs_from_codes, default_litlen_costs,
// deflate_choose_default_litlen_costs, deflate_default_length_cost,
// deflate_default_offset_slot_cost, deflate_set_default_costs,
// deflate_adjust_cost, deflate_adjust_costs_impl, deflate_adjust_costs and
// deflate_set_initial_costs).
//
// Costs are in units of 1/BIT_COST bits. All arithmetic is C's u32
// arithmetic, except the u64 statistics comparison of deflate_adjust_costs.

package deflate

// deflateCosts holds the costs of the near-optimal parsing algorithm.
// C: struct deflate_costs
type deflateCosts struct {
	// The cost to output each possible literal. C: literal
	literal [deflateNumLiterals]uint32

	// The cost to output each possible match length. C: length
	length [deflateMaxMatchLen + 1]uint32

	// The cost to output a match offset of each possible offset slot.
	// C: offset_slot
	offsetSlot [deflateNumOffsetSyms]uint32
}

// computeTrueCost returns the exact cost, in bits, that would be required
// to output the matches and literals described by c.freqs as a dynamic
// Huffman block; the litlen and offset codes must already be built in
// c.codes. Like C it precomputes the dynamic header (c.o.precode) and then
// clears the litlen lengths from numLitlenSyms on (where the header
// precomputation left a copy of the offset lengths), so that c.codes.lens
// is again what the code builder produced.
// port of deflate_compute_true_cost
func (c *Compressor) computeTrueCost() uint32 {
	cost := uint32(0)

	c.precomputeHuffmanHeader()
	pc := &c.o.precode

	clear(c.codes.lens[pc.numLitlenSyms:deflateNumLitlenSyms])

	cost += 5 + 5 + 4 + (3 * pc.numExplicitLens)
	for sym := range deflateNumPrecodeSyms {
		cost += pc.freqs[sym] * (uint32(pc.lens[sym]) + uint32(deflateExtraPrecodeBits[sym]))
	}

	sym := 0
	for ; sym < deflateFirstLenSym; sym++ {
		cost += c.freqs.litlen[sym] * uint32(c.codes.lens[sym])
	}

	for ; sym < deflateFirstLenSym+len(deflateExtraLengthBits); sym++ {
		cost += c.freqs.litlen[sym] *
			(uint32(c.codes.lens[sym]) + uint32(deflateExtraLengthBits[sym-deflateFirstLenSym]))
	}

	for sym = 0; sym < len(deflateExtraOffsetBits); sym++ {
		cost += c.freqs.offset[sym] *
			(uint32(c.codes.lens[lensOffsetStart+sym]) + uint32(deflateExtraOffsetBits[sym]))
	}
	return cost
}

// setCostsFromCodes sets the current cost model (c.pnState.costs) from the
// codeword lengths lens (C's struct deflate_lens: the 288 litlen lengths,
// then the 32 offset lengths); a length 0 (an unused symbol) costs the
// *_NOSTAT_BITS. port of deflate_set_costs_from_codes
func (c *Compressor) setCostsFromCodes(lens *[deflateNumLitlenSyms + deflateNumOffsetSyms]uint8) {
	costs := &c.pnState.costs

	// Literals
	for i := range deflateNumLiterals {
		bits := uint32(lens[i])
		if bits == 0 {
			bits = literalNostatBits
		}
		costs.literal[i] = bits * bitCost
	}

	// Lengths
	for i := deflateMinMatchLen; i <= deflateMaxMatchLen; i++ {
		lengthSlot := deflateLengthSlot[i]
		litlenSym := deflateFirstLenSym + int(lengthSlot)
		bits := uint32(lens[litlenSym])
		if bits == 0 {
			bits = lengthNostatBits
		}
		bits += uint32(deflateExtraLengthBits[lengthSlot])
		costs.length[i] = bits * bitCost
	}

	// Offset slots
	for i := range len(deflateOffsetSlotBase) {
		bits := uint32(lens[lensOffsetStart+i])
		if bits == 0 {
			bits = offsetNostatBits
		}
		bits += uint32(deflateExtraOffsetBits[i])
		costs.offsetSlot[i] = bits * bitCost
	}
}

// defaultLitlenCosts gives the default cost of a literal symbol and of a
// length symbol, depending on the characteristics of the input data (C
// generated it with scripts/gen_default_litlen_costs.py). It is indexed
// first by the estimated match probability (0: the data doesn't contain
// many matches, match_prob 0.25; 1: neutral, 0.50; 2: lots of matches,
// 0.75); usedLitsToLitCost maps the number of distinct used literals (0 is
// copied from 1) to int(-log2((1 - match_prob) / num_used_literals) *
// BIT_COST), and lenSymCost is int(-log2(match_prob/NUM_LEN_SLOTS) *
// BIT_COST). C: default_litlen_costs
var defaultLitlenCosts = [3]struct {
	usedLitsToLitCost [257]uint8 // C: used_lits_to_lit_cost
	lenSymCost        uint8      // C: len_sym_cost
}{
	{ // match_prob = 0.25
		usedLitsToLitCost: [257]uint8{
			6, 6, 22, 32, 38, 43, 48, 51, 54, 57, 59, 61, 64, 65, 67, 69,
			70, 72, 73, 74, 75, 76, 77, 79, 80, 80, 81, 82, 83, 84, 85, 85,
			86, 87, 88, 88, 89, 89, 90, 91, 91, 92, 92, 93, 93, 94, 95, 95,
			96, 96, 96, 97, 97, 98, 98, 99, 99, 99, 100, 100, 101, 101, 101, 102,
			102, 102, 103, 103, 104, 104, 104, 105, 105, 105, 105, 106, 106, 106, 107, 107,
			107, 108, 108, 108, 108, 109, 109, 109, 109, 110, 110, 110, 111, 111, 111, 111,
			112, 112, 112, 112, 112, 113, 113, 113, 113, 114, 114, 114, 114, 114, 115, 115,
			115, 115, 115, 116, 116, 116, 116, 116, 117, 117, 117, 117, 117, 118, 118, 118,
			118, 118, 118, 119, 119, 119, 119, 119, 120, 120, 120, 120, 120, 120, 121, 121,
			121, 121, 121, 121, 121, 122, 122, 122, 122, 122, 122, 123, 123, 123, 123, 123,
			123, 123, 124, 124, 124, 124, 124, 124, 124, 125, 125, 125, 125, 125, 125, 125,
			125, 126, 126, 126, 126, 126, 126, 126, 127, 127, 127, 127, 127, 127, 127, 127,
			128, 128, 128, 128, 128, 128, 128, 128, 128, 129, 129, 129, 129, 129, 129, 129,
			129, 129, 130, 130, 130, 130, 130, 130, 130, 130, 130, 131, 131, 131, 131, 131,
			131, 131, 131, 131, 131, 132, 132, 132, 132, 132, 132, 132, 132, 132, 132, 133,
			133, 133, 133, 133, 133, 133, 133, 133, 133, 134, 134, 134, 134, 134, 134, 134,
			134,
		},
		lenSymCost: 109,
	},
	{ // match_prob = 0.5
		usedLitsToLitCost: [257]uint8{
			16, 16, 32, 41, 48, 53, 57, 60, 64, 66, 69, 71, 73, 75, 76, 78,
			80, 81, 82, 83, 85, 86, 87, 88, 89, 90, 91, 92, 92, 93, 94, 95,
			96, 96, 97, 98, 98, 99, 99, 100, 101, 101, 102, 102, 103, 103, 104, 104,
			105, 105, 106, 106, 107, 107, 108, 108, 108, 109, 109, 110, 110, 110, 111, 111,
			112, 112, 112, 113, 113, 113, 114, 114, 114, 115, 115, 115, 115, 116, 116, 116,
			117, 117, 117, 118, 118, 118, 118, 119, 119, 119, 119, 120, 120, 120, 120, 121,
			121, 121, 121, 122, 122, 122, 122, 122, 123, 123, 123, 123, 124, 124, 124, 124,
			124, 125, 125, 125, 125, 125, 126, 126, 126, 126, 126, 127, 127, 127, 127, 127,
			128, 128, 128, 128, 128, 128, 129, 129, 129, 129, 129, 129, 130, 130, 130, 130,
			130, 130, 131, 131, 131, 131, 131, 131, 131, 132, 132, 132, 132, 132, 132, 133,
			133, 133, 133, 133, 133, 133, 134, 134, 134, 134, 134, 134, 134, 134, 135, 135,
			135, 135, 135, 135, 135, 135, 136, 136, 136, 136, 136, 136, 136, 136, 137, 137,
			137, 137, 137, 137, 137, 137, 138, 138, 138, 138, 138, 138, 138, 138, 138, 139,
			139, 139, 139, 139, 139, 139, 139, 139, 140, 140, 140, 140, 140, 140, 140, 140,
			140, 141, 141, 141, 141, 141, 141, 141, 141, 141, 141, 142, 142, 142, 142, 142,
			142, 142, 142, 142, 142, 142, 143, 143, 143, 143, 143, 143, 143, 143, 143, 143,
			144,
		},
		lenSymCost: 93,
	},
	{ // match_prob = 0.75
		usedLitsToLitCost: [257]uint8{
			32, 32, 48, 57, 64, 69, 73, 76, 80, 82, 85, 87, 89, 91, 92, 94,
			96, 97, 98, 99, 101, 102, 103, 104, 105, 106, 107, 108, 108, 109, 110, 111,
			112, 112, 113, 114, 114, 115, 115, 116, 117, 117, 118, 118, 119, 119, 120, 120,
			121, 121, 122, 122, 123, 123, 124, 124, 124, 125, 125, 126, 126, 126, 127, 127,
			128, 128, 128, 129, 129, 129, 130, 130, 130, 131, 131, 131, 131, 132, 132, 132,
			133, 133, 133, 134, 134, 134, 134, 135, 135, 135, 135, 136, 136, 136, 136, 137,
			137, 137, 137, 138, 138, 138, 138, 138, 139, 139, 139, 139, 140, 140, 140, 140,
			140, 141, 141, 141, 141, 141, 142, 142, 142, 142, 142, 143, 143, 143, 143, 143,
			144, 144, 144, 144, 144, 144, 145, 145, 145, 145, 145, 145, 146, 146, 146, 146,
			146, 146, 147, 147, 147, 147, 147, 147, 147, 148, 148, 148, 148, 148, 148, 149,
			149, 149, 149, 149, 149, 149, 150, 150, 150, 150, 150, 150, 150, 150, 151, 151,
			151, 151, 151, 151, 151, 151, 152, 152, 152, 152, 152, 152, 152, 152, 153, 153,
			153, 153, 153, 153, 153, 153, 154, 154, 154, 154, 154, 154, 154, 154, 154, 155,
			155, 155, 155, 155, 155, 155, 155, 155, 156, 156, 156, 156, 156, 156, 156, 156,
			156, 157, 157, 157, 157, 157, 157, 157, 157, 157, 157, 158, 158, 158, 158, 158,
			158, 158, 158, 158, 158, 158, 159, 159, 159, 159, 159, 159, 159, 159, 159, 159,
			160,
		},
		lenSymCost: 84,
	},
}

// The table is for BIT_COST 16. C: STATIC_ASSERT(BIT_COST == 16)
const _ = uint(bitCost-16) + uint(16-bitCost)

// chooseDefaultLitlenCosts chooses the default costs for literal and
// length symbols (both part of the litlen alphabet) for the block: from the
// number of distinct literals used more than block_length >> 11 times, and
// a match probability estimated from the matchfinding's greedy match length
// frequencies (c.pnState.matchLenFreqs, counted from the min_len heuristic's
// length on). Like C it leaves the block's literal counts in
// c.freqs.litlen[0:256] (scratch space).
// port of deflate_choose_default_litlen_costs
func (c *Compressor) chooseDefaultLitlenCosts(block []byte) (litCost, lenSymCost uint32) {
	numUsedLiterals := uint32(0)
	literalFreq := uint32(len(block))

	// Calculate the number of distinct literals that exist in the data.
	clear(c.freqs.litlen[:deflateNumLiterals])
	cutoff := literalFreq >> 11 // Ignore literals used very rarely.
	for _, b := range block {
		c.freqs.litlen[b]++
	}
	for _, f := range c.freqs.litlen[:deflateNumLiterals] {
		if f > cutoff {
			numUsedLiterals++
		}
	}
	if numUsedLiterals == 0 {
		numUsedLiterals = 1
	}

	// Estimate the relative frequency of literals and matches in the
	// optimal parsing solution: basically the match frequency of a greedy
	// parse, with the min_len heuristic of the greedy and lazy parsers so
	// that not too many matches are counted when literals are cheaper than
	// short matches.
	matchFreq := uint32(0)
	freqs := &c.pnState.matchLenFreqs
	for i := chooseMinMatchLen(numUsedLiterals, c.maxSearchDepth); i < uint32(len(freqs)); i++ {
		matchFreq += freqs[i]
		literalFreq -= i * freqs[i]
	}
	if int32(literalFreq) < 0 { // shouldn't happen
		literalFreq = 0
	}

	var i int
	if matchFreq > literalFreq {
		i = 2 // many matches
	} else if matchFreq*4 > literalFreq {
		i = 1 // neutral
	} else {
		i = 0 // few matches
	}

	return uint32(defaultLitlenCosts[i].usedLitsToLitCost[numUsedLiterals]),
		uint32(defaultLitlenCosts[i].lenSymCost)
}

// deflateDefaultLengthCost returns the default cost of a match length:
// the length symbol cost plus its extra bits. port of
// deflate_default_length_cost
func deflateDefaultLengthCost(ln, lenSymCost uint32) uint32 {
	slot := deflateLengthSlot[ln]
	numExtraBits := uint32(deflateExtraLengthBits[slot])

	return lenSymCost + (numExtraBits * bitCost)
}

// deflateDefaultOffsetSlotCost returns the default cost of an offset slot:
// all 30 potentially-used offset symbols are assumed equally probable,
// int(-log2(1/30) * BIT_COST), plus the extra bits.
// port of deflate_default_offset_slot_cost
func deflateDefaultOffsetSlotCost(slot int) uint32 {
	numExtraBits := uint32(deflateExtraOffsetBits[slot])
	const offsetSymCost = 4*bitCost + (907*bitCost)/1000

	return offsetSymCost + (numExtraBits * bitCost)
}

// setDefaultCosts sets the default symbol costs for the first block's
// first optimization pass. port of deflate_set_default_costs
func (c *Compressor) setDefaultCosts(litCost, lenSymCost uint32) {
	costs := &c.pnState.costs

	// Literals
	for i := range deflateNumLiterals {
		costs.literal[i] = litCost
	}

	// Lengths
	for i := uint32(deflateMinMatchLen); i <= deflateMaxMatchLen; i++ {
		costs.length[i] = deflateDefaultLengthCost(i, lenSymCost)
	}

	// Offset slots
	for i := range len(deflateOffsetSlotBase) {
		costs.offsetSlot[i] = deflateDefaultOffsetSlotCost(i)
	}
}

// deflateAdjustCost mixes the default cost into *cost: the higher
// changeAmount (0..3), the more weight the default cost gets.
// port of deflate_adjust_cost
func deflateAdjustCost(cost *uint32, defaultCost uint32, changeAmount int) {
	switch changeAmount {
	case 0:
		// Block is very similar to previous; prefer previous costs.
		*cost = (defaultCost + 3**cost) / 4
	case 1:
		*cost = (defaultCost + *cost) / 2
	case 2:
		*cost = (5*defaultCost + 3**cost) / 8
	default:
		// Block differs greatly from previous; prefer default costs.
		*cost = (3*defaultCost + *cost) / 4
	}
}

// adjustCostsImpl mixes the default costs into every cost of the current
// cost model. port of deflate_adjust_costs_impl
func (c *Compressor) adjustCostsImpl(litCost, lenSymCost uint32, changeAmount int) {
	costs := &c.pnState.costs

	// Literals
	for i := range deflateNumLiterals {
		deflateAdjustCost(&costs.literal[i], litCost, changeAmount)
	}

	// Lengths
	for i := uint32(deflateMinMatchLen); i <= deflateMaxMatchLen; i++ {
		deflateAdjustCost(&costs.length[i], deflateDefaultLengthCost(i, lenSymCost), changeAmount)
	}

	// Offset slots
	for i := range len(deflateOffsetSlotBase) {
		deflateAdjustCost(&costs.offsetSlot[i], deflateDefaultOffsetSlotCost(i), changeAmount)
	}
}

// adjustCosts adjusts the costs when beginning a new (not the first)
// block. The current costs are optimized for the previous block's data;
// depending on how different the new block's literal/match statistics
// (c.splitStats) are from the previous block's (saved by
// nearOptimalSaveStats), they are mixed with the default costs or replaced
// by them. The comparison is the end-of-block check's, on two whole blocks,
// in u64. port of deflate_adjust_costs
func (c *Compressor) adjustCosts(litCost, lenSymCost uint32) {
	ns := c.pnState
	totalDelta := uint64(0)

	for i := range numObservationTypes {
		prev := uint64(ns.prevObservations[i]) * uint64(c.splitStats.numObservations)
		cur := uint64(c.splitStats.observations[i]) * uint64(ns.prevNumObservations)

		if prev > cur {
			totalDelta += prev - cur
		} else {
			totalDelta += cur - prev
		}
	}
	cutoff := (uint64(ns.prevNumObservations) * uint64(c.splitStats.numObservations) * 200) / 512

	switch {
	case totalDelta > 3*cutoff:
		// Big change in the data; just use the default costs.
		c.setDefaultCosts(litCost, lenSymCost)
	case 4*totalDelta > 9*cutoff:
		c.adjustCostsImpl(litCost, lenSymCost, 3)
	case 2*totalDelta > 3*cutoff:
		c.adjustCostsImpl(litCost, lenSymCost, 2)
	case 2*totalDelta > cutoff:
		c.adjustCostsImpl(litCost, lenSymCost, 1)
	default:
		c.adjustCostsImpl(litCost, lenSymCost, 0)
	}
}

// setInitialCosts sets the costs of a block's first optimization pass: the
// default costs for the first block, else the previous costs adjusted.
// port of deflate_set_initial_costs
func (c *Compressor) setInitialCosts(block []byte, isFirstBlock bool) {
	litCost, lenSymCost := c.chooseDefaultLitlenCosts(block)
	if isFirstBlock {
		c.setDefaultCosts(litCost, lenSymCost)
	} else {
		c.adjustCosts(litCost, lenSymCost)
	}
}

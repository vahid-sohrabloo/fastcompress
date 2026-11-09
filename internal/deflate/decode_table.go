// Port of libdeflate v1.26 lib/deflate_decompress.c (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the decode table entry formats and static
// decode results, struct libdeflate_decompressor, make_decode_table_entry,
// build_decode_table, build_precode_decode_table, build_litlen_decode_table
// and build_offset_decode_table.
//
// The builders keep C's algorithm step for step, so the table contents are
// bit-identical to libdeflate's, including the entries of the two accepted
// incomplete codes, subtable placement and the table-doubling fill pattern
// (decode_table_test.go compares whole tables with ref/ldref dump-tables).

package deflate

import "math/bits"

// Precode decode table entry format (bits not described are zero):
//
//	Bit 20-16:  presym
//	Bit 10-8:   codeword length [not used]
//	Bit 2-0:    codeword length
//
// The precode table never has subtables (precodeTablebits ==
// deflateMaxPreCodewordLen). precodeDecodeResults holds the static part of
// each entry; makeDecodeTableEntry adds the length.
// C: precode_decode_results
var precodeDecodeResults = [deflateNumPrecodeSyms]uint32{
	0 << 16, 1 << 16, 2 << 16, 3 << 16,
	4 << 16, 5 << 16, 6 << 16, 7 << 16,
	8 << 16, 9 << 16, 10 << 16, 11 << 16,
	12 << 16, 13 << 16, 14 << 16, 15 << 16,
	16 << 16, 17 << 16, 18 << 16,
}

// Litlen decode table entry format (bits not described are zero):
//
//	Literals:
//		Bit 31:     1 (huffdecLiteral)
//		Bit 23-16:  literal value
//		Bit 15:     0 (!huffdecExceptional)
//		Bit 14:     0 (!huffdecSubtablePointer)
//		Bit 13:     0 (!huffdecEndOfBlock)
//		Bit 11-8:   remaining codeword length [not used]
//		Bit 3-0:    remaining codeword length
//	Lengths:
//		Bit 31:     0 (!huffdecLiteral)
//		Bit 24-16:  length base value
//		Bit 15:     0 (!huffdecExceptional)
//		Bit 14:     0 (!huffdecSubtablePointer)
//		Bit 13:     0 (!huffdecEndOfBlock)
//		Bit 11-8:   remaining codeword length
//		Bit 4-0:    remaining codeword length + number of extra bits
//	End of block:
//		Bit 31:     0 (!huffdecLiteral)
//		Bit 15:     1 (huffdecExceptional)
//		Bit 14:     0 (!huffdecSubtablePointer)
//		Bit 13:     1 (huffdecEndOfBlock)
//		Bit 11-8:   remaining codeword length [not used]
//		Bit 3-0:    remaining codeword length
//	Subtable pointer:
//		Bit 31:     0 (!huffdecLiteral)
//		Bit 30-16:  index of start of subtable
//		Bit 15:     1 (huffdecExceptional)
//		Bit 14:     1 (huffdecSubtablePointer)
//		Bit 13:     0 (!huffdecEndOfBlock)
//		Bit 11-8:   number of subtable bits
//		Bit 3-0:    number of main table bits
//
// The low byte is the number of bits to remove from the bitstream (codeword
// plus extra bits), so the decoder can do 'bitsleft -= entry'. See the C
// comment above litlen_decode_results for the rationale of the layout.
// litlenDecodeResults holds the static part of each entry.
// C: litlen_decode_results
var litlenDecodeResults = [deflateNumLitlenSyms]uint32{
	// Literals: C ENTRY(literal) = HUFFDEC_LITERAL | literal<<16.
	huffdecLiteral | 0<<16, huffdecLiteral | 1<<16, huffdecLiteral | 2<<16, huffdecLiteral | 3<<16,
	huffdecLiteral | 4<<16, huffdecLiteral | 5<<16, huffdecLiteral | 6<<16, huffdecLiteral | 7<<16,
	huffdecLiteral | 8<<16, huffdecLiteral | 9<<16, huffdecLiteral | 10<<16, huffdecLiteral | 11<<16,
	huffdecLiteral | 12<<16, huffdecLiteral | 13<<16, huffdecLiteral | 14<<16, huffdecLiteral | 15<<16,
	huffdecLiteral | 16<<16, huffdecLiteral | 17<<16, huffdecLiteral | 18<<16, huffdecLiteral | 19<<16,
	huffdecLiteral | 20<<16, huffdecLiteral | 21<<16, huffdecLiteral | 22<<16, huffdecLiteral | 23<<16,
	huffdecLiteral | 24<<16, huffdecLiteral | 25<<16, huffdecLiteral | 26<<16, huffdecLiteral | 27<<16,
	huffdecLiteral | 28<<16, huffdecLiteral | 29<<16, huffdecLiteral | 30<<16, huffdecLiteral | 31<<16,
	huffdecLiteral | 32<<16, huffdecLiteral | 33<<16, huffdecLiteral | 34<<16, huffdecLiteral | 35<<16,
	huffdecLiteral | 36<<16, huffdecLiteral | 37<<16, huffdecLiteral | 38<<16, huffdecLiteral | 39<<16,
	huffdecLiteral | 40<<16, huffdecLiteral | 41<<16, huffdecLiteral | 42<<16, huffdecLiteral | 43<<16,
	huffdecLiteral | 44<<16, huffdecLiteral | 45<<16, huffdecLiteral | 46<<16, huffdecLiteral | 47<<16,
	huffdecLiteral | 48<<16, huffdecLiteral | 49<<16, huffdecLiteral | 50<<16, huffdecLiteral | 51<<16,
	huffdecLiteral | 52<<16, huffdecLiteral | 53<<16, huffdecLiteral | 54<<16, huffdecLiteral | 55<<16,
	huffdecLiteral | 56<<16, huffdecLiteral | 57<<16, huffdecLiteral | 58<<16, huffdecLiteral | 59<<16,
	huffdecLiteral | 60<<16, huffdecLiteral | 61<<16, huffdecLiteral | 62<<16, huffdecLiteral | 63<<16,
	huffdecLiteral | 64<<16, huffdecLiteral | 65<<16, huffdecLiteral | 66<<16, huffdecLiteral | 67<<16,
	huffdecLiteral | 68<<16, huffdecLiteral | 69<<16, huffdecLiteral | 70<<16, huffdecLiteral | 71<<16,
	huffdecLiteral | 72<<16, huffdecLiteral | 73<<16, huffdecLiteral | 74<<16, huffdecLiteral | 75<<16,
	huffdecLiteral | 76<<16, huffdecLiteral | 77<<16, huffdecLiteral | 78<<16, huffdecLiteral | 79<<16,
	huffdecLiteral | 80<<16, huffdecLiteral | 81<<16, huffdecLiteral | 82<<16, huffdecLiteral | 83<<16,
	huffdecLiteral | 84<<16, huffdecLiteral | 85<<16, huffdecLiteral | 86<<16, huffdecLiteral | 87<<16,
	huffdecLiteral | 88<<16, huffdecLiteral | 89<<16, huffdecLiteral | 90<<16, huffdecLiteral | 91<<16,
	huffdecLiteral | 92<<16, huffdecLiteral | 93<<16, huffdecLiteral | 94<<16, huffdecLiteral | 95<<16,
	huffdecLiteral | 96<<16, huffdecLiteral | 97<<16, huffdecLiteral | 98<<16, huffdecLiteral | 99<<16,
	huffdecLiteral | 100<<16, huffdecLiteral | 101<<16, huffdecLiteral | 102<<16, huffdecLiteral | 103<<16,
	huffdecLiteral | 104<<16, huffdecLiteral | 105<<16, huffdecLiteral | 106<<16, huffdecLiteral | 107<<16,
	huffdecLiteral | 108<<16, huffdecLiteral | 109<<16, huffdecLiteral | 110<<16, huffdecLiteral | 111<<16,
	huffdecLiteral | 112<<16, huffdecLiteral | 113<<16, huffdecLiteral | 114<<16, huffdecLiteral | 115<<16,
	huffdecLiteral | 116<<16, huffdecLiteral | 117<<16, huffdecLiteral | 118<<16, huffdecLiteral | 119<<16,
	huffdecLiteral | 120<<16, huffdecLiteral | 121<<16, huffdecLiteral | 122<<16, huffdecLiteral | 123<<16,
	huffdecLiteral | 124<<16, huffdecLiteral | 125<<16, huffdecLiteral | 126<<16, huffdecLiteral | 127<<16,
	huffdecLiteral | 128<<16, huffdecLiteral | 129<<16, huffdecLiteral | 130<<16, huffdecLiteral | 131<<16,
	huffdecLiteral | 132<<16, huffdecLiteral | 133<<16, huffdecLiteral | 134<<16, huffdecLiteral | 135<<16,
	huffdecLiteral | 136<<16, huffdecLiteral | 137<<16, huffdecLiteral | 138<<16, huffdecLiteral | 139<<16,
	huffdecLiteral | 140<<16, huffdecLiteral | 141<<16, huffdecLiteral | 142<<16, huffdecLiteral | 143<<16,
	huffdecLiteral | 144<<16, huffdecLiteral | 145<<16, huffdecLiteral | 146<<16, huffdecLiteral | 147<<16,
	huffdecLiteral | 148<<16, huffdecLiteral | 149<<16, huffdecLiteral | 150<<16, huffdecLiteral | 151<<16,
	huffdecLiteral | 152<<16, huffdecLiteral | 153<<16, huffdecLiteral | 154<<16, huffdecLiteral | 155<<16,
	huffdecLiteral | 156<<16, huffdecLiteral | 157<<16, huffdecLiteral | 158<<16, huffdecLiteral | 159<<16,
	huffdecLiteral | 160<<16, huffdecLiteral | 161<<16, huffdecLiteral | 162<<16, huffdecLiteral | 163<<16,
	huffdecLiteral | 164<<16, huffdecLiteral | 165<<16, huffdecLiteral | 166<<16, huffdecLiteral | 167<<16,
	huffdecLiteral | 168<<16, huffdecLiteral | 169<<16, huffdecLiteral | 170<<16, huffdecLiteral | 171<<16,
	huffdecLiteral | 172<<16, huffdecLiteral | 173<<16, huffdecLiteral | 174<<16, huffdecLiteral | 175<<16,
	huffdecLiteral | 176<<16, huffdecLiteral | 177<<16, huffdecLiteral | 178<<16, huffdecLiteral | 179<<16,
	huffdecLiteral | 180<<16, huffdecLiteral | 181<<16, huffdecLiteral | 182<<16, huffdecLiteral | 183<<16,
	huffdecLiteral | 184<<16, huffdecLiteral | 185<<16, huffdecLiteral | 186<<16, huffdecLiteral | 187<<16,
	huffdecLiteral | 188<<16, huffdecLiteral | 189<<16, huffdecLiteral | 190<<16, huffdecLiteral | 191<<16,
	huffdecLiteral | 192<<16, huffdecLiteral | 193<<16, huffdecLiteral | 194<<16, huffdecLiteral | 195<<16,
	huffdecLiteral | 196<<16, huffdecLiteral | 197<<16, huffdecLiteral | 198<<16, huffdecLiteral | 199<<16,
	huffdecLiteral | 200<<16, huffdecLiteral | 201<<16, huffdecLiteral | 202<<16, huffdecLiteral | 203<<16,
	huffdecLiteral | 204<<16, huffdecLiteral | 205<<16, huffdecLiteral | 206<<16, huffdecLiteral | 207<<16,
	huffdecLiteral | 208<<16, huffdecLiteral | 209<<16, huffdecLiteral | 210<<16, huffdecLiteral | 211<<16,
	huffdecLiteral | 212<<16, huffdecLiteral | 213<<16, huffdecLiteral | 214<<16, huffdecLiteral | 215<<16,
	huffdecLiteral | 216<<16, huffdecLiteral | 217<<16, huffdecLiteral | 218<<16, huffdecLiteral | 219<<16,
	huffdecLiteral | 220<<16, huffdecLiteral | 221<<16, huffdecLiteral | 222<<16, huffdecLiteral | 223<<16,
	huffdecLiteral | 224<<16, huffdecLiteral | 225<<16, huffdecLiteral | 226<<16, huffdecLiteral | 227<<16,
	huffdecLiteral | 228<<16, huffdecLiteral | 229<<16, huffdecLiteral | 230<<16, huffdecLiteral | 231<<16,
	huffdecLiteral | 232<<16, huffdecLiteral | 233<<16, huffdecLiteral | 234<<16, huffdecLiteral | 235<<16,
	huffdecLiteral | 236<<16, huffdecLiteral | 237<<16, huffdecLiteral | 238<<16, huffdecLiteral | 239<<16,
	huffdecLiteral | 240<<16, huffdecLiteral | 241<<16, huffdecLiteral | 242<<16, huffdecLiteral | 243<<16,
	huffdecLiteral | 244<<16, huffdecLiteral | 245<<16, huffdecLiteral | 246<<16, huffdecLiteral | 247<<16,
	huffdecLiteral | 248<<16, huffdecLiteral | 249<<16, huffdecLiteral | 250<<16, huffdecLiteral | 251<<16,
	huffdecLiteral | 252<<16, huffdecLiteral | 253<<16, huffdecLiteral | 254<<16, huffdecLiteral | 255<<16,

	// End of block.
	huffdecExceptional | huffdecEndOfBlock,

	// Lengths: C ENTRY(length_base, num_extra_bits) = length_base<<16 | num_extra_bits.
	3<<16 | 0, 4<<16 | 0, 5<<16 | 0, 6<<16 | 0,
	7<<16 | 0, 8<<16 | 0, 9<<16 | 0, 10<<16 | 0,
	11<<16 | 1, 13<<16 | 1, 15<<16 | 1, 17<<16 | 1,
	19<<16 | 2, 23<<16 | 2, 27<<16 | 2, 31<<16 | 2,
	35<<16 | 3, 43<<16 | 3, 51<<16 | 3, 59<<16 | 3,
	67<<16 | 4, 83<<16 | 4, 99<<16 | 4, 115<<16 | 4,
	131<<16 | 5, 163<<16 | 5, 195<<16 | 5, 227<<16 | 5,
	258<<16 | 0, 258<<16 | 0, 258<<16 | 0,
}

// Offset decode table entry format (bits not described are zero):
//
//	Offsets:
//		Bit 31-16:  offset base value
//		Bit 15:     0 (!huffdecExceptional)
//		Bit 14:     0 (!huffdecSubtablePointer)
//		Bit 11-8:   remaining codeword length
//		Bit 4-0:    remaining codeword length + number of extra bits
//	Subtable pointer:
//		Bit 31-16:  index of start of subtable
//		Bit 15:     1 (huffdecExceptional)
//		Bit 14:     1 (huffdecSubtablePointer)
//		Bit 11-8:   number of subtable bits
//		Bit 3-0:    number of main table bits
//
// These work like the length and subtable pointer entries of the litlen
// table. C: offset_decode_results (ENTRY(offset_base, num_extra_bits) =
// offset_base<<16 | num_extra_bits)
var offsetDecodeResults = [deflateNumOffsetSyms]uint32{
	1<<16 | 0, 2<<16 | 0, 3<<16 | 0, 4<<16 | 0,
	5<<16 | 1, 7<<16 | 1, 9<<16 | 2, 13<<16 | 2,
	17<<16 | 3, 25<<16 | 3, 33<<16 | 4, 49<<16 | 4,
	65<<16 | 5, 97<<16 | 5, 129<<16 | 6, 193<<16 | 6,
	257<<16 | 7, 385<<16 | 7, 513<<16 | 8, 769<<16 | 8,
	1025<<16 | 9, 1537<<16 | 9, 2049<<16 | 10, 3073<<16 | 10,
	4097<<16 | 11, 6145<<16 | 11, 8193<<16 | 12, 12289<<16 | 12,
	16385<<16 | 13, 24577<<16 | 13, 24577<<16 | 13, 24577<<16 | 13,
}

// Decompressor holds the decode tables and the temporary arrays used to
// build them; the rest of the decompression state lives in locals, as in C
// (libdeflate only does whole-buffer decompression). Keeping the tables here
// lets the static-code tables be reused across consecutive static blocks,
// even across streams, and lets a pooled Decompressor build every table
// without allocating. The zero value is ready to use.
// C: struct libdeflate_decompressor
//
// Layout versus C. C overlays, in 'union u', precode_lens with lens (the
// same first 19 bytes), the pair {lens, precode_decode_table} (u.l) with
// litlen_decode_table: lens is dead once the offset table and then the
// litlen table have been built from it (build_decode_table reads all lens
// before writing the table), and the precode table is dead once lens is
// filled. Go cannot overlay byte and uint32 arrays without unsafe (not
// allowed in decoder files), so:
//   - precode_lens is lens[:deflateNumPrecodeSyms] (the same bytes as in C);
//   - lens, precodeDecodeTable and litlenDecodeTable are separate arrays.
//
// The semantics are the same as long as the decoder keeps C's order (precode
// table, then lens, then offset table, then litlen table). The one visible
// difference: table entries a build does not write keep this struct's
// previous contents instead of C's union residue (lens bytes); no decoder
// lookup reaches them.
//
// Memory/cache layout (for Phase 4 to revisit with profiles): the struct is
// 960 bytes larger than C's (12528 vs sizeof 11568 on amd64 and 386; C's
// includes the 8-byte free_func), because lens and the precode table are no
// longer overlaid on the litlen table. The two tables the decode loop reads
// come first, litlen then offset, adjacent as in C: litlen at offset 0 (9368
// bytes), offset table at 9368, the same offset as in C (not 64-byte
// aligned: 9368 = 146*64 + 24), then the build-time-only arrays. Go only
// guarantees 8-byte alignment for the struct itself.
type Decompressor struct {
	// C: u.litlen_decode_table
	litlenDecodeTable [litlenEnough]uint32
	// C: offset_decode_table
	offsetDecodeTable [offsetEnough]uint32
	// C: u.l.precode_decode_table
	precodeDecodeTable [precodeEnough]uint32
	// C: u.l.lens; lens[:deflateNumPrecodeSyms] is C's u.precode_lens.
	lens [deflateNumLitlenSyms + deflateNumOffsetSyms + deflateMaxLensOverrun]uint8
	// Used only during buildDecodeTable. C: sorted_syms
	sortedSyms [deflateMaxNumSyms]uint16
	// C: static_codes_loaded
	staticCodesLoaded bool
	// C: litlen_tablebits
	litlenTablebits uint32
}

// makeDecodeTableEntry combines the static part decodeResults[sym] with the
// dynamic part len (the remaining codeword length: the full length for main
// table entries, length minus table bits for subtable entries) by adding len
// to each of the two low-order bytes.
// port of make_decode_table_entry
func makeDecodeTableEntry(decodeResults []uint32, sym, ln uint32) uint32 {
	return decodeResults[sym] + (ln << 8) + ln
}

// buildDecodeTable builds a table for decoding symbols of the canonical
// Huffman code given by lens[:numSyms] (codeword length per symbol, 0 =
// unused), indexed by bit-reversed codewords (the low-order bit is the first
// bit of the codeword), as DEFLATE uses.
//
// decodeTable must be large enough (see the ENOUGH constants). Every length
// must be <= maxCodewordLen <= deflateMaxCodewordLen; the caller guarantees
// it (precode lengths are 3-bit fields, litlen/offset lengths come from
// presyms 0..15), otherwise the lengths are untrusted. A length above
// deflateMaxCodewordLen panics on the lenCounts index; a length in
// (maxCodewordLen, deflateMaxCodewordLen] (only possible for the precode,
// 8..15) is counted but then ignored (their symbols overwrite the start of
// sortedSyms), giving an unspecified table, as C's read of uninitialized
// len_counts would (unreachable from real input).
// lens may be read in full before decodeTable is written, so the two could
// overlap as in C. decodeResults gives the static part of each
// entry. tableBits is log2 of the main table size; with tableBitsRet != nil
// it is a maximum, lowered to the longest codeword used, and the result is
// stored in *tableBitsRet. sortedSyms is scratch of at least numSyms
// entries.
//
// It returns false, without writing decodeTable, if the lengths do not form
// a valid code: an over-subscribed code, or an incomplete code other than
// the two accepted ones (no codewords, or one codeword of length 1), which
// are built as a complete code giving one symbol both 1-bit codewords.
// port of build_decode_table
func buildDecodeTable(decodeTable []uint32, lens []uint8, numSyms uint32,
	decodeResults []uint32, tableBits, maxCodewordLen uint32,
	sortedSyms []uint16, tableBitsRet *uint32) bool {
	var lenCounts [deflateMaxCodewordLen + 1]uint32
	var offsets [deflateMaxCodewordLen + 1]uint32
	var sym uint32            // current symbol
	var codeword uint32       // current codeword, bit-reversed
	var ln uint32             // current codeword length in bits (C: len)
	var count uint32          // num codewords remaining with this length
	var codespaceUsed uint32  // codespace used out of 2^maxCodewordLen
	var curTableEnd uint32    // end index of current table
	var subtablePrefix uint32 // codeword prefix of current subtable
	var subtableStart uint32  // start index of current subtable
	var subtableBits uint32   // log2 of current subtable length

	// Count how many codewords have each length, including 0. (lenCounts
	// is zeroed by its declaration; C zeroes [0, maxCodewordLen].)
	lens = lens[:numSyms]
	for _, l := range lens {
		lenCounts[l]++
	}

	// Determine the actual maximum codeword length that was used, and
	// decrease tableBits to it if allowed.
	for maxCodewordLen > 1 && lenCounts[maxCodewordLen] == 0 {
		maxCodewordLen--
	}
	if tableBitsRet != nil {
		tableBits = min(tableBits, maxCodewordLen)
		*tableBitsRet = tableBits
	}

	// Sort the symbols primarily by increasing codeword length and
	// secondarily by increasing symbol value (that is, by codeword in
	// lexicographic order, since the code is canonical). Compute
	// codespaceUsed in the same pass over lenCounts. It cannot overflow:
	// 288 * 2^14 < 2^32 (C STATIC_ASSERTs this).
	offsets[0] = 0
	offsets[1] = lenCounts[0]
	codespaceUsed = 0
	for ln = 1; ln < maxCodewordLen; ln++ {
		offsets[ln+1] = offsets[ln] + lenCounts[ln]
		codespaceUsed = (codespaceUsed << 1) + lenCounts[ln]
	}
	codespaceUsed = (codespaceUsed << 1) + lenCounts[ln]

	for s, l := range lens {
		// The mask is the identity: every length is at most
		// deflateMaxCodewordLen, or the count loop above has panicked.
		// It only lets the compiler drop the bounds check on offsets.
		l &= deflateMaxCodewordLen
		sortedSyms[offsets[l]] = uint16(s)
		offsets[l]++
	}

	si := offsets[0] // skip unused symbols (C: sorted_syms += offsets[0])

	// lens is done being used; decodeTable may be written from here on.

	// Over-subscribed code?
	if codespaceUsed > 1<<maxCodewordLen {
		return false
	}

	// Incomplete code? The RFC allows an incomplete offset code with one
	// codeword of length 1, or with no codewords; libdeflate accepts the
	// same for the litlen and pre codes. Each accepted case is mapped to a
	// complete code with only real symbols: the used symbol (one codeword)
	// or symbol 0 (no codewords) gets both codewords '0' and '1'. Every
	// other incomplete code is rejected (like zlib: more than one
	// codeword, or a codeword longer than 1).
	if codespaceUsed < 1<<maxCodewordLen {
		if codespaceUsed == 0 {
			sym = 0 // arbitrary
		} else {
			if codespaceUsed != 1<<(maxCodewordLen-1) || lenCounts[1] != 1 {
				return false
			}
			sym = uint32(sortedSyms[si])
		}
		entry := makeDecodeTableEntry(decodeResults, sym, 1)
		t := decodeTable[:1<<tableBits]
		for i := range t {
			t[i] = entry
		}
		return true
	}

	// The lengths form a complete code. Enumerate the codewords in
	// lexicographic order and fill the entries of each.
	//
	// First, the codewords with len <= tableBits; each gets
	// 2^(tableBits-len) main table entries, spaced 2^len apart (codewords
	// are bit-reversed). Instead of strided stores, the table is treated as
	// having 2^len entries while codewords of length len are processed, and
	// each time len grows the table size is doubled by copying the first
	// half to the second. Copied entries that are not initialized yet will
	// be initialized later, since the code is complete.
	codeword = 0
	ln = 1
	for {
		count = lenCounts[ln]
		if count != 0 {
			break
		}
		ln++
	}
	curTableEnd = 1 << ln
	for ln <= tableBits {
		// Process all 'count' codewords with length 'len' bits.
		for {
			// Fill the first entry for the current codeword.
			decodeTable[codeword] = makeDecodeTableEntry(decodeResults, uint32(sortedSyms[si]), ln)
			si++

			if codeword == curTableEnd-1 {
				// Last codeword (all 1's).
				for ; ln < tableBits; ln++ {
					copy(decodeTable[curTableEnd:2*curTableEnd], decodeTable[:curTableEnd])
					curTableEnd <<= 1
				}
				return true
			}
			// Advance to the lexicographically next codeword: increment
			// the bit-reversed codeword (set its highest-order 0 bit,
			// clear the 1 bits above it; C: bsr32 of the flipped
			// codeword), then append 0's (a no-op when bit-reversed).
			bit := uint32(1) << (bits.Len32(codeword^(curTableEnd-1)) - 1)
			codeword &= bit - 1
			codeword |= bit
			count--
			if count == 0 {
				break
			}
		}

		// Advance to the next codeword length.
		for {
			ln++
			if ln <= tableBits {
				copy(decodeTable[curTableEnd:2*curTableEnd], decodeTable[:curTableEnd])
				curTableEnd <<= 1
			}
			count = lenCounts[ln]
			if count != 0 {
				break
			}
		}
	}

	// Process codewords with len > tableBits. These require subtables.
	curTableEnd = 1 << tableBits
	subtablePrefix = ^uint32(0) // C: -1
	subtableStart = 0
	for {
		// Start a new subtable if the first tableBits bits of the
		// codeword don't match the prefix of the current subtable.
		if codeword&(1<<tableBits-1) != subtablePrefix {
			subtablePrefix = codeword & (1<<tableBits - 1)
			subtableStart = curTableEnd
			// A codeword of length tableBits+n needs a 2^n-entry
			// subtable, or more: while fewer than 2^n codewords of that
			// length remain, bring in longer codewords until the
			// subtable can be filled completely (always possible, the
			// code being complete).
			subtableBits = ln - tableBits
			codespaceUsed = count
			for codespaceUsed < 1<<subtableBits {
				subtableBits++
				codespaceUsed = (codespaceUsed << 1) + lenCounts[tableBits+subtableBits]
			}
			curTableEnd = subtableStart + 1<<subtableBits

			// The entry that points from the main table to the
			// subtable.
			decodeTable[subtablePrefix] = subtableStart<<16 |
				huffdecExceptional |
				huffdecSubtablePointer |
				subtableBits<<8 | tableBits
		}

		// Fill the subtable entries for the current codeword.
		entry := makeDecodeTableEntry(decodeResults, uint32(sortedSyms[si]), ln-tableBits)
		si++
		i := subtableStart + (codeword >> tableBits)
		stride := uint32(1) << (ln - tableBits)
		for {
			decodeTable[i] = entry
			i += stride
			if i >= curTableEnd {
				break
			}
		}

		// Advance to the next codeword.
		if codeword == 1<<ln-1 { // last codeword (all 1's)?
			return true
		}
		bit := uint32(1) << (bits.Len32(codeword^(1<<ln-1)) - 1)
		codeword &= bit - 1
		codeword |= bit
		count--
		for count == 0 {
			ln++
			count = lenCounts[ln]
		}
	}
}

// buildPrecodeDecodeTable builds the precode decode table from the precode
// lengths in d.lens[:deflateNumPrecodeSyms] (C's u.precode_lens).
// port of build_precode_decode_table
func (d *Decompressor) buildPrecodeDecodeTable() bool {
	return buildDecodeTable(d.precodeDecodeTable[:],
		d.lens[:deflateNumPrecodeSyms],
		deflateNumPrecodeSyms,
		precodeDecodeResults[:],
		precodeTablebits,
		deflateMaxPreCodewordLen,
		d.sortedSyms[:],
		nil)
}

// buildLitlenDecodeTable builds the litlen decode table from
// d.lens[:numLitlenSyms]. Besides the litlen table it writes
// d.litlenTablebits (also when it returns false, as in C) and the
// d.sortedSyms scratch; lens is left intact (C: the litlen table overlays
// lens and overwrites it, so the offset table must be built first; keep
// that order).
// port of build_litlen_decode_table
func (d *Decompressor) buildLitlenDecodeTable(numLitlenSyms, numOffsetSyms uint32) bool {
	_ = numOffsetSyms // unused, as in C
	return buildDecodeTable(d.litlenDecodeTable[:],
		d.lens[:],
		numLitlenSyms,
		litlenDecodeResults[:],
		litlenTablebits,
		deflateMaxLitlenCodewordLen,
		d.sortedSyms[:],
		&d.litlenTablebits)
}

// buildOffsetDecodeTable builds the offset decode table from
// d.lens[numLitlenSyms : numLitlenSyms+numOffsetSyms].
// port of build_offset_decode_table
func (d *Decompressor) buildOffsetDecodeTable(numLitlenSyms, numOffsetSyms uint32) bool {
	return buildDecodeTable(d.offsetDecodeTable[:],
		d.lens[numLitlenSyms:],
		numOffsetSyms,
		offsetDecodeResults[:],
		offsetTablebits,
		deflateMaxOffsetCodewordLen,
		d.sortedSyms[:],
		nil)
}

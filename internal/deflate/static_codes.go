// Port of libdeflate v1.26 lib/decompress_template.h (Copyright 2016 Eric
// Biggers, MIT; see NOTICE): the static Huffman code setup of a
// BTYPE=01 block (the static_codes_loaded handling and the static lens).

package deflate

// loadStaticCodes prepares the decode tables of a static Huffman block. If
// they already hold the static codes (an earlier static block, with no
// dynamic block in between, possibly in an earlier stream), it returns true
// and the caller uses them as they are (C: goto have_decode_tables).
// Otherwise it marks them loaded, sets the static code lengths in d.lens and
// returns false with numLitlenSyms = 288 and numOffsetSyms = 32; the caller
// then builds the offset and litlen tables as for a dynamic block. A dynamic
// block must clear d.staticCodesLoaded, since it overwrites the tables.
// port of the static Huffman block part of decompress_template.h (FUNCNAME)
func (d *Decompressor) loadStaticCodes() (loaded bool, numLitlenSyms, numOffsetSyms uint32) {
	if d.staticCodesLoaded {
		return true, 0, 0
	}
	d.staticCodesLoaded = true

	var i int
	for i = 0; i < 144; i++ {
		d.lens[i] = 8
	}
	for ; i < 256; i++ {
		d.lens[i] = 9
	}
	for ; i < 280; i++ {
		d.lens[i] = 7
	}
	for ; i < 288; i++ {
		d.lens[i] = 8
	}

	for ; i < 288+32; i++ {
		d.lens[i] = 5
	}

	return false, deflateNumLitlenSyms, deflateNumOffsetSyms
}

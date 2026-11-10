package zstd

import (
	"encoding/binary"
	"slices"

	"github.com/vahid-sohrabloo/fastcompress/internal/fse"
	"github.com/vahid-sohrabloo/fastcompress/internal/huf"
	"github.com/vahid-sohrabloo/fastcompress/internal/xxhash"
)

// Decoder dictionaries. C: lib/decompress/zstd_ddict.c, and the dictionary
// parts of zstd_decompress.c (v1.5.7).

// ddict is a digested decoder dictionary. It is never written after
// createDDict, so contexts share it. C: zstd_ddict.c:ZSTD_DDict
type ddict struct {
	// content is C's dictContent and dictSize: a private copy of the whole
	// dictionary, entropy header included (C's DDict references it all as
	// history, unlike ZSTD_decompress_insertDictionary).
	content        []byte
	entropy        entropyDTables
	entropyPresent bool
	dictID         uint32
}

// createDDict digests dict, a zstd dictionary or (raw, or not starting with
// the dictionary magic number) raw content. A zstd dictionary whose tables
// or repcodes are invalid returns an error.
// C: zstd_ddict.c:ZSTD_createDDict_advanced (ZSTD_dlm_byCopy; ZSTD_dct_auto,
// or ZSTD_dct_rawContent when raw) with ZSTD_initDDict_internal
func createDDict(dict []byte, raw bool) (*ddict, error) {
	dd := &ddict{content: slices.Clip(slices.Clone(dict))}
	dd.entropy.hufTable.Init(hufDTableCapacityLog) // C: hufTable[0] = ZSTD_HUFFDTABLE_CAPACITY_LOG * 0x1000001
	if e := dd.loadEntropy(raw); e != 0 {
		return nil, errDictionaryCorrupted
	}
	return dd, nil
}

// loadEntropy parses the dictionary's header, if it has one.
// C: zstd_ddict.c:ZSTD_loadEntropy_intoDDict (ZSTD_dct_fullDict is not
// used)
func (dd *ddict) loadEntropy(raw bool) dErr {
	dd.dictID = 0
	dd.entropyPresent = false
	if raw {
		return 0
	}
	if len(dd.content) < 8 {
		return 0 // pure content mode
	}
	if binary.LittleEndian.Uint32(dd.content) != dictMagic {
		return 0 // pure content mode
	}
	dd.dictID = binary.LittleEndian.Uint32(dd.content[frameIDSize:])

	// load entropy tables
	if _, e := loadDEntropy(&dd.entropy, dd.content); e != 0 {
		return dErrDictionaryCorrupted
	}
	dd.entropyPresent = true
	return 0
}

// loadDEntropy loads the entropy tables and repcodes of the zstd
// dictionary dict (which starts with the magic number) into entropy and
// returns the size of its header: magic, ID, tables and repcodes.
// C: zstd_decompress.c:ZSTD_loadDEntropy (C lends the FSE tables to
// HUF_readDTableX2_wksp as its workspace; huf.ReadDTableX2 has its own)
func loadDEntropy(entropy *entropyDTables, dict []byte) (int, dErr) {
	if len(dict) <= 8 {
		return 0, dErrDictionaryCorrupted // dict is too small
	}
	dictPtr := 8 // skip header = magic + dictID

	hSize, err := huf.ReadDTableX2(&entropy.hufTable, dict[dictPtr:], 0)
	if err != nil {
		return 0, dErrDictionaryCorrupted
	}
	dictPtr += hSize

	for _, t := range []struct {
		dt       []seqSymbol
		max, log uint
		base     []uint32
		bits     []uint8
	}{
		{entropy.ofTable[:], maxOff, offFSELog, ofBase[:], ofBits[:]},
		{entropy.mlTable[:], maxML, mlFSELog, mlBase[:], mlBits[:]},
		{entropy.llTable[:], maxLL, llFSELog, llBase[:], llBits[:]},
	} {
		var nCount [maxML + 1]int16 // maxML is the largest of the three
		maxValue := t.max
		log, headerSize, err := fse.ReadNCount(nCount[:t.max+1], &maxValue, dict[dictPtr:])
		if err != nil || maxValue > t.max || log > t.log {
			return 0, dErrDictionaryCorrupted
		}
		buildFSETable(t.dt, nCount[:], maxValue, t.base, t.bits, log, &entropy.workspace)
		dictPtr += headerSize
	}

	if dictPtr+12 > len(dict) {
		return 0, dErrDictionaryCorrupted
	}
	dictContentSize := uint64(len(dict) - (dictPtr + 12))
	for i := range repNum {
		rep := binary.LittleEndian.Uint32(dict[dictPtr:])
		dictPtr += 4
		if rep == 0 || uint64(rep) > dictContentSize {
			return 0, dErrDictionaryCorrupted
		}
		entropy.rep[i] = rep
	}
	return dictPtr, 0
}

// refDictContent makes dict the current prefix, and the previous one the
// extDict. C: zstd_decompress.c:ZSTD_refDictContent
func (d *dctx) refDictContent(dict []byte) {
	d.extDict = d.prevDst[d.prefixStart:d.previousDstEnd] // C: dictEnd = previousDstEnd
	d.dictEnd = d.extDict
	d.virtualStart = -len(d.extDict)
	d.prevDst = slices.Clip(dict)
	d.prefixStart = 0
	d.previousDstEnd = len(dict)
}

// insertDictionary loads dict into d: a zstd dictionary's tables and
// repcodes, and the content after them (all of dict for raw content) as
// the prefix. C: zstd_decompress.c:ZSTD_decompress_insertDictionary
func (d *dctx) insertDictionary(dict []byte) dErr {
	if len(dict) < 8 || binary.LittleEndian.Uint32(dict) != dictMagic {
		d.refDictContent(dict) // pure content mode
		return 0
	}
	d.dictID = binary.LittleEndian.Uint32(dict[frameIDSize:])

	// load entropy tables
	eSize, e := loadDEntropy(&d.entropy, dict)
	if e != 0 {
		return dErrDictionaryCorrupted
	}
	dict = dict[eSize:]
	d.litEntropy, d.fseEntropy = true, true

	// reference dictionary content
	d.refDictContent(dict)
	return 0
}

// copyDDictParameters starts a frame from dd: its content as the prefix,
// and its tables (used in place) and repcodes when it has them.
// C: zstd_ddict.c:ZSTD_copyDDictParameters
func (d *dctx) copyDDictParameters(dd *ddict) {
	d.dictID = dd.dictID
	d.prevDst = dd.content
	d.prefixStart, d.virtualStart = 0, 0
	d.dictEnd = dd.content
	d.previousDstEnd = len(dd.content)
	if dd.entropyPresent {
		d.litEntropy, d.fseEntropy = true, true
		d.llTptr = dd.entropy.llTable[:]
		d.mlTptr = dd.entropy.mlTable[:]
		d.ofTptr = dd.entropy.ofTable[:]
		d.hufPtr = &dd.entropy.hufTable
		d.entropy.rep = dd.entropy.rep
	} else {
		d.litEntropy, d.fseEntropy = false, false
	}
}

// decompressBeginUsingDDict resets d for a new frame using dd (nil: no
// dictionary). C: zstd_decompress.c:ZSTD_decompressBegin_usingDDict
func (d *dctx) decompressBeginUsingDDict(dd *ddict) {
	if dd != nil {
		// C: dctx->dictEnd != dictEnd (the dictionary is "hot" when the
		// previous frame ended its use of it). Go compares where the slices
		// end; an empty dictionary is always cold.
		d.ddictIsCold = !sameEnd(d.dictEnd, dd.content)
	}
	d.decompressBegin()
	if dd != nil { // NULL ddict is equivalent to no dictionary
		d.copyDDictParameters(dd)
	}
}

// sameEnd reports whether a and b end at the same byte of the same array.
func sameEnd(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[len(a)-1] == &b[len(b)-1]
}

// Multiple DDicts hash set. C: zstd_decompress.c
const (
	ddictHashSetMaxLoadFactorCountMult = 4 // C: DDICT_HASHSET_MAX_LOAD_FACTOR_COUNT_MULT
	ddictHashSetMaxLoadFactorSizeMult  = 3 // C: DDICT_HASHSET_MAX_LOAD_FACTOR_SIZE_MULT
	ddictHashSetTableBaseSize          = 64
	ddictHashSetResizeFactor           = 2
)

// ddictSet holds dictionaries by ID: an open-addressing table whose size is
// a power of 2. C: zstd_decompress_internal.h:ZSTD_DDictHashSet
// (ddictPtrTableSize is len(table))
type ddictSet struct {
	table []*ddict // C: ddictPtrTable
	count int      // C: ddictPtrCount
}

// newDDictSet. C: zstd_decompress.c:ZSTD_createDDictHashSet
func newDDictSet() *ddictSet {
	return &ddictSet{table: make([]*ddict, ddictHashSetTableBaseSize)}
}

// getIndex returns the slot where the search for dictID starts.
// C: zstd_decompress.c:ZSTD_DDictHashSet_getIndex (the ID's bytes in
// little-endian order, as C's on little-endian machines)
func (s *ddictSet) getIndex(dictID uint32) int {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], dictID)
	return int(xxhash.Sum64(b[:], 0) & uint64(len(s.table)-1))
}

// emplace adds dd without resizing, replacing a dictionary with the same
// ID. C: zstd_decompress.c:ZSTD_DDictHashSet_emplaceDDict
func (s *ddictSet) emplace(dd *ddict) dErr {
	dictID := dd.dictID
	idx := s.getIndex(dictID)
	idxRangeMask := len(s.table) - 1
	if s.count == len(s.table) {
		return dErrGeneric // Hash set is full!
	}
	for s.table[idx] != nil {
		// Replace existing ddict if inserting ddict with same dictID
		if s.table[idx].dictID == dictID {
			s.table[idx] = dd
			return 0
		}
		// C: idx &= idxRangeMask; idx++, which reads one slot past the
		// end of the table (and skips slot 0) after the last one. Go
		// wraps to slot 0.
		idx = (idx + 1) & idxRangeMask
	}
	s.table[idx] = dd
	s.count++
	return 0
}

// expand doubles the table and rehashes every dictionary.
// C: zstd_decompress.c:ZSTD_DDictHashSet_expand
func (s *ddictSet) expand() {
	oldTable := s.table
	s.table = make([]*ddict, len(oldTable)*ddictHashSetResizeFactor)
	s.count = 0
	for _, dd := range oldTable {
		if dd != nil {
			s.emplace(dd) // cannot fail: the new table has room
		}
	}
}

// get returns the dictionary with ID dictID, or nil. As C, the search stops
// at the first slot holding nil or a dictionary with ID 0, and returns it.
// C: zstd_decompress.c:ZSTD_DDictHashSet_getDDict
func (s *ddictSet) get(dictID uint32) *ddict {
	idx := s.getIndex(dictID)
	idxRangeMask := len(s.table) - 1
	for {
		var currDictID uint32 // C: ZSTD_getDictID_fromDDict(NULL) is 0
		if s.table[idx] != nil {
			currDictID = s.table[idx].dictID
		}
		if currDictID == dictID || currDictID == 0 {
			// currDictID == 0 implies a NULL ddict entry
			break
		}
		idx = (idx + 1) & idxRangeMask // C: see emplace
	}
	return s.table[idx]
}

// add adds dd, growing the table first when it is a quarter full or more.
// C: zstd_decompress.c:ZSTD_DDictHashSet_addDDict (C's comment says a
// 0.75 load factor; its integer arithmetic gives 0.25)
func (s *ddictSet) add(dd *ddict) {
	if s.count*ddictHashSetMaxLoadFactorCountMult/len(s.table)*ddictHashSetMaxLoadFactorSizeMult != 0 {
		s.expand()
	}
	s.emplace(dd) // cannot fail: the table is less than full
}

// selectFrameDDict returns the dictionary of a frame with dictionary ID
// dictID: the one registered with that ID, or nil (no dictionary: a
// frame with an ID then fails with dictionary_wrong).
// C: zstd_decompress.c:ZSTD_DCtx_selectFrameDDict, with the selection rule
// above.
// Deviations from C:
//   - C keeps the current DDict when the ID is not found, so a frame
//     without an ID decodes with the last dictionary used; Go uses none.
//   - C's lookup (get) stops at the first dictionary with ID 0 and returns
//     it, which shadows a dictionary placed after it on the probe path.
//     Go's probes the same slots (emplace's order) but stops only at an
//     empty slot or the exact ID; the load factor (at most 0.25) ensures an
//     empty slot.
func (s *ddictSet) selectFrameDDict(dictID uint32) *ddict {
	idxRangeMask := len(s.table) - 1
	for idx := s.getIndex(dictID); s.table[idx] != nil; idx = (idx + 1) & idxRangeMask {
		if s.table[idx].dictID == dictID {
			return s.table[idx]
		}
	}
	return nil
}

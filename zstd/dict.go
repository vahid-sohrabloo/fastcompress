package zstd

import (
	"encoding/binary"

	"github.com/vahid-sohrabloo/fastzstd/huff0"
	"github.com/vahid-sohrabloo/fastzstd/internal/huf"
)

// dictInfo is InspectDictionary's result.
type dictInfo struct {
	id      uint32
	content []byte
	offsets [3]int
	lit     *huff0.Scratch
}

func (i *dictInfo) ID() uint32                 { return i.id }
func (i *dictInfo) ContentSize() int           { return len(i.content) }
func (i *dictInfo) Content() []byte            { return i.content }
func (i *dictInfo) Offsets() [3]int            { return i.offsets }
func (i *dictInfo) LitEncoder() *huff0.Scratch { return i.lit }

// InspectDictionary loads a zstd dictionary and provides functions to inspect
// the content.
//
// b must be in zstd dictionary format: raw content (no dictionary magic
// number) is an error, as is a dictionary the decoder rejects (C's
// ZSTD_loadDEntropy: invalid tables or repcodes). ID is the dictionary ID
// (C's ZSTD_getDictID_fromDict), Content the bytes after the header (a
// sub-slice of b), Offsets the repcodes. LitEncoder holds the literal
// Huffman table (C's HUF_readCTable); it is nil when the table cannot be
// read as a compression table.
func InspectDictionary(b []byte) (interface {
	ID() uint32
	ContentSize() int
	Content() []byte
	Offsets() [3]int
	LitEncoder() *huff0.Scratch
}, error) {
	if len(b) < 8 || binary.LittleEndian.Uint32(b) != dictMagic {
		return nil, errDictionaryWrong
	}
	var entropy entropyDTables
	entropy.hufTable.Init(hufDTableCapacityLog)
	eSize, e := loadDEntropy(&entropy, b)
	if e != 0 {
		return nil, errDictionaryCorrupted
	}
	info := &dictInfo{id: binary.LittleEndian.Uint32(b[frameIDSize:]), content: b[eSize:]}
	for i, r := range entropy.rep {
		info.offsets[i] = int(r)
	}
	ct := make(huf.CTable, huf.CTableSize(huf.SymbolValueMax))
	maxSymbolValue := uint(huf.SymbolValueMax)
	if _, _, err := huf.ReadCTable(ct, &maxSymbolValue, b[8:]); err == nil {
		info.lit = huf.NewScratch(ct).(*huff0.Scratch)
	}
	return info, nil
}

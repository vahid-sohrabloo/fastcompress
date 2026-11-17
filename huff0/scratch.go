package huff0

import "github.com/vahid-sohrabloo/fastcompress/internal/huf"

// Scratch holds a Huffman compression table (a dictionary's literal
// table, from zstd.InspectDictionary). Its fields are unexported.
type Scratch struct {
	ct huf.CTable
}

func init() {
	huf.NewScratch = func(ct huf.CTable) any { return &Scratch{ct: ct} }
}

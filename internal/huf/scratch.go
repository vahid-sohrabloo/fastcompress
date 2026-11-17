package huf

// NewScratch wraps a compression table in a *huff0.Scratch, whose fields
// only package huff0 can set. Package huff0 sets this in its init; any
// package that names huff0.Scratch imports huff0, so it is set before use.
var NewScratch func(CTable) any

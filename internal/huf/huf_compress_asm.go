//go:build (amd64 || arm64) && !appengine && !noasm && !purego && gc

// The Go side of the Huffman stream writer's assembly, shared by amd64 and
// arm64.

package huf

func compress1XBody(dst, src []byte, ct CTable) int {
	if !useHufAsm || len(dst) <= 8 || ReadCTableHeader(ct).TableLog > TableLogMax {
		return compress1XBodyGeneric(dst, src, ct)
	}
	var srcPtr *byte
	if len(src) > 0 {
		srcPtr = &src[0]
	}
	elts := (*[SymbolValueMax + 1]CElt)(ct[1:])
	return compress1XAsmVariant(&dst[0], len(dst), srcPtr, len(src), &elts[0])
}

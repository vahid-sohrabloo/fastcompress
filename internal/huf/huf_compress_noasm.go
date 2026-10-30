//go:build (!amd64 && !arm64) || appengine || noasm || purego || !gc

package huf

func compress1XBody(dst, src []byte, ct CTable) int { return compress1XBodyGeneric(dst, src, ct) }

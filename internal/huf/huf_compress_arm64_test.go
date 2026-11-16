//go:build arm64 && !appengine && !noasm && !purego && gc

package huf

func hufAsmVariants() []hufVariant {
	always := func() bool { return true }
	return []hufVariant{
		{"base", compress1XAsm, TableLogMax, always, false},
		{"x5", compress1XAsmX5, 11, always, false},
		{"x10", compress1XAsmX10, 11, always, true},
	}
}

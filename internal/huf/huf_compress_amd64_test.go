//go:build amd64 && !appengine && !noasm && !purego && gc

package huf

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

func hufAsmVariants() []hufVariant {
	always := func() bool { return true }
	bmi2 := func() bool { return cpu.X86.HasBMI2 }
	return []hufVariant{
		{"base", compress1XAsm, TableLogMax, always, false},
		{"bmi2", compress1XAsmBMI2, TableLogMax, bmi2, false},
		{"bmi2x5", compress1XAsmBMI2x5, 11, bmi2, false},
		{"bmi2x10", compress1XAsmBMI2x10, 11, bmi2, true},
	}
}

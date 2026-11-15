package lz4

import (
	"os"

	"github.com/vahid-sohrabloo/fastcompress/internal/cpu"
)

// FASTCOMPRESS_FUZZ_ASM=off|nobmi2|on (scripts/fuzz.sh) switches every
// assembly routine of the test binary. go test caches results without keying
// on it, so seed runs need -count=1.
func init() {
	if err := cpu.SetAsmMode(os.Getenv("FASTCOMPRESS_FUZZ_ASM")); err != nil {
		panic(err)
	}
}

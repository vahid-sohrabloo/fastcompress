package fse

import (
	"os"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
)

// FASTZSTD_FUZZ_ASM=off|nobmi2|on (scripts/fuzz.sh, .github/workflows/fuzz.yml)
// switches every assembly routine of the test binary, including those
// registered after this init runs. go test caches results without keying
// on it (init runs before the test log), so seed runs need -count=1.
func init() {
	if err := cpu.SetAsmMode(os.Getenv("FASTZSTD_FUZZ_ASM")); err != nil {
		panic(err)
	}
}

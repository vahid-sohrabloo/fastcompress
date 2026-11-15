package lz4

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// sortedInputs returns the differential inputs in name order.
func sortedInputs(t *testing.T) ([]string, map[string][]byte) {
	m := creftest.LZ4Inputs(t)
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, m
}

func TestCompressFastCref(t *testing.T) {
	names, m := sortedInputs(t)
	accels := []int{1, 2, 7, 65537}
	if testing.Short() {
		accels = []int{1, 3}
	}
	for _, name := range names {
		in := m[name]
		for _, a := range accels {
			t.Run(fmt.Sprintf("%s/a%d", name, a), func(t *testing.T) {
				want := creftest.LZ4Run(t, in, "fast", "accel="+strconv.Itoa(a))
				dst := make([]byte, CompressBound(len(in)))
				n := CompressFast(dst, in, a)
				if !bytes.Equal(dst[:n], want) {
					t.Fatalf("output differs from C: %d bytes, C %d", n, len(want))
				}
			})
		}
	}
}

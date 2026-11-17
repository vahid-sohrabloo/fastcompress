//go:build amd64 && !appengine && !noasm && !purego && gc

package deflate

import "testing"

// TestHCLongestMatchAsmUnused pins that amd64 never takes the
// search-only assembly path: useHCSearchAsm is false (the whole greedy and
// lazy loops are in assembly), and the stub panics if it is ever called.
func TestHCLongestMatchAsmUnused(t *testing.T) {
	if useHCSearchAsm {
		t.Fatal("useHCSearchAsm is true on amd64")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("hcLongestMatchAsm did not panic")
		}
	}()
	hcLongestMatchAsm(nil, nil, nil, 0, 0, 0, 0, 0, nil)
}

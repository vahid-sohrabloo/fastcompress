//go:build !(linux || darwin)

package guardmem

import "testing"

// Real reports whether After and Before place buffers next to guard pages.
func Real() bool { return false }

// After returns make([]byte, n): this platform has no guard pages.
func After(t testing.TB, n int) []byte { return make([]byte, n) }

// Before returns make([]byte, n): this platform has no guard pages.
func Before(t testing.TB, n int) []byte { return make([]byte, n) }

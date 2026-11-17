// Package guardmem allocates test buffers that sit flush against an
// inaccessible (PROT_NONE) page, so that an out-of-bounds access by code
// that escapes Go's bounds checks (assembly, or unsafe pointer arithmetic)
// faults instead of silently reading or corrupting neighbouring memory.
//
// After returns a buffer whose end touches a guard page, Before one whose
// start does. On platforms without guard pages (Real reports false) both
// return an ordinary make([]byte, n). The mappings are released by
// t.Cleanup, so a buffer must not be used after its test ends.
package guardmem

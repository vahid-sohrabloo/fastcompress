//go:build race

package zlib_test

// raceEnabled reports whether the race detector is on (allocation counts
// are not checked under it).
const raceEnabled = true

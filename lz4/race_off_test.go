//go:build !race

package lz4

// raceEnabled reports whether the race detector is on (allocation counts
// are not checked under it).
const raceEnabled = false

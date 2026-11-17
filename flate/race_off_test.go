//go:build !race

package flate_test

// raceEnabled reports whether the race detector is on (allocation counts
// are not checked under it).
const raceEnabled = false

//go:build race

package gzip_test

// raceEnabled reports whether the race detector is on (allocation counts
// are not checked under it).
const raceEnabled = true

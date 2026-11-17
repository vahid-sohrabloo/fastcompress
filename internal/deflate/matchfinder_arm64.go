//go:build arm64 && !appengine && !noasm && !purego && gc

package deflate

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useMatchfinderSSE2 selects the NEON matchfinderInitSSE2 /
// matchfinderRebaseSSE2 (the names are amd64's; C: matchfinder_init_neon
// and matchfinder_rebase_neon of lib/arm/matchfinder_impl.h). NEON is part
// of arm64, so it is always available; the switch exists for the assembly
// equivalence tests.
var useMatchfinderSSE2 bool

func init() {
	cpu.RegisterAsm("deflate.matchfinderSSE2", &useMatchfinderSSE2, func() bool { return true })
}

// matchfinderInitSSE2 sets data[:len(data)&^31] to MATCHFINDER_INITVAL,
// 32 entries (four 16-byte stores) at a time. port of matchfinder_init_neon
//
//go:noescape
func matchfinderInitSSE2(data []mfPos)

// matchfinderRebaseSSE2 rebases data[:len(data)&^31] (a subtraction of
// MATCHFINDER_WINDOW_SIZE with signed saturation), 32 entries at a time.
// port of matchfinder_rebase_neon
//
//go:noescape
func matchfinderRebaseSSE2(data []mfPos)

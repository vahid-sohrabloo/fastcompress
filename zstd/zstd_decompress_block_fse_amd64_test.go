//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/vahid-sohrabloo/fastzstd/internal/cpu"
	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
	"github.com/vahid-sohrabloo/fastzstd/internal/fse"
	"github.com/vahid-sohrabloo/fastzstd/internal/guardmem"
)

// fseTableCase is one buildFSETable input: the counts (norm[:maxSV+1]),
// the table log and the symbol type's baseValue and nbAdditionalBits.
type fseTableCase struct {
	norm     []int16
	tableLog uint
	base     []uint32
	bits     []uint8
}

// fseCodeTables returns the LL, ML or OF code tables for k%3, or the ML
// ones when maxSV is too large for the chosen type.
func fseCodeTables(k int, maxSV int) ([]uint32, []uint8) {
	switch {
	case k%3 == 0 && maxSV <= maxLL:
		return llBase[:], llBits[:]
	case k%3 == 1 && maxSV <= maxOff:
		return ofBase[:], ofBits[:]
	}
	return mlBase[:], mlBits[:]
}

// validFSENorm reports whether norm is a distribution buildFSETableAsm
// accepts: every count >= -1, summing (-1 as 1) to 1 << tableLog.
func validFSENorm(norm []int16, tableLog uint) bool {
	sum := 0
	for _, c := range norm {
		switch {
		case c < -1:
			return false
		case c == -1:
			sum++
		default:
			sum += int(c)
		}
	}
	return sum == 1<<tableLog
}

// randomFSENorm returns random valid counts for maxSV+1 symbols at
// tableLog, with low-probability (-1) symbols when lowProb (as
// TestBuildFSETableBound's generator).
func randomFSENorm(rng *rand.Rand, maxSV int, tableLog uint, lowProb bool) []int16 {
	size := 1 << tableLog
	norm := make([]int16, maxSV+1)
	remaining := size
	var positive []int
	for s := range norm {
		r := rng.IntN(10)
		switch {
		case r < 3 || remaining == 0:
		case lowProb && r < 5:
			norm[s] = -1
			remaining--
		default:
			norm[s] = 1
			remaining--
			positive = append(positive, s)
		}
	}
	if remaining > 0 && len(positive) == 0 {
		if norm[0] == 0 {
			remaining--
		}
		norm[0] = 1
		positive = append(positive, 0)
	}
	for remaining > 0 {
		s := positive[rng.IntN(len(positive))]
		add := 1 + rng.IntN(remaining)
		norm[s] += int16(add)
		remaining -= add
	}
	return norm
}

// guardSlice returns n elements of T in guard-page memory: ending at a
// guard page (guardmem.After), or starting at one when before.
func guardSlice[T any](t testing.TB, n int, before bool) []T {
	size := int(unsafe.Sizeof(*new(T)))
	var b []byte
	if before {
		b = guardmem.Before(t, max(n, 1)*size)
	} else {
		b = guardmem.After(t, max(n, 1)*size)
	}
	return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), max(n, 1))[:n]
}

// guardWksp returns a buildFSETableWksp in guard-page memory.
func guardWksp(t testing.TB, before bool) *buildFSETableWksp {
	return &guardSlice[buildFSETableWksp](t, 1, before)[0]
}

// fillFSEState fills dt and wksp with the same bytes for every seed, so the
// two builds start alike (and the assembly cannot lean on zeroed memory).
func fillFSEState(dt []seqSymbol, wksp *buildFSETableWksp, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 1))
	for i := range dt {
		v := rng.Uint64()
		dt[i] = seqSymbol{nextState: uint16(v), nbAdditionalBits: uint8(v >> 16), nbBits: uint8(v >> 24), baseValue: uint32(v >> 32)}
	}
	for i := range wksp.symbolNext {
		wksp.symbolNext[i] = uint16(rng.Uint32())
	}
	for i := range wksp.spread {
		wksp.spread[i] = byte(rng.Uint32())
	}
}

// fseSeed returns fillFSEState's seed for c: its table log and every count
// (FNV-1a), so cases differ in starting garbage, not only in length.
func fseSeed(c fseTableCase) uint64 {
	h := uint64(14695981039346656037) ^ uint64(c.tableLog)
	for _, v := range c.norm {
		h = (h ^ uint64(uint16(v))) * 1099511628211
	}
	return h
}

// checkFSETableAsm builds c's table with the Go body and with
// buildFSETableAsm (its inputs and outputs against guard pages: after one,
// then before one) from the same starting state, and fails t unless the
// assembly accepts c and leaves the same table and workspace, which must
// pass checkTableBound. With the assembly off (no BMI2, or
// FASTZSTD_FUZZ_ASM) it builds with Go twice.
func checkFSETableAsm(t testing.TB, what string, c fseTableCase) {
	t.Helper()
	maxSV1 := uint(len(c.norm))
	var want [1 + 1<<maxFSELog]seqSymbol
	var wantW buildFSETableWksp
	fillFSEState(want[:], &wantW, fseSeed(c))
	func() {
		saved := useFSETableAsm
		defer func() { useFSETableAsm = saved }()
		useFSETableAsm = false
		buildFSETable(want[:], c.norm, maxSV1-1, c.base, c.bits, c.tableLog, &wantW)
	}()
	for _, before := range []bool{false, true} {
		dt := guardSlice[seqSymbol](t, 1+1<<c.tableLog, before)
		norm := guardSlice[int16](t, len(c.norm), before)
		base := guardSlice[uint32](t, int(maxSV1), !before)
		bits := guardSlice[uint8](t, int(maxSV1), before)
		wksp := guardWksp(t, before)
		copy(norm, c.norm)
		copy(base, c.base)
		copy(bits, c.bits)
		var full [1 + 1<<maxFSELog]seqSymbol
		fillFSEState(full[:], wksp, fseSeed(c))
		copy(dt, full[:])
		if useFSETableAsm {
			if !buildFSETableAsm(&dt[0], &norm[0], maxSV1, &base[0], &bits[0], c.tableLog, wksp) {
				t.Fatalf("%s: the assembly rejected norm %v at tableLog %d", what, c.norm, c.tableLog)
			}
		} else {
			buildFSETable(dt, norm, maxSV1-1, base, bits, c.tableLog, wksp)
		}
		for i := range dt {
			if dt[i] != want[i] {
				t.Fatalf("%s: norm %v tableLog %d: cell %d asm %+v, Go %+v", what, c.norm, c.tableLog, i-1, dt[i], want[i])
			}
		}
		if *wksp != wantW {
			t.Fatalf("%s: norm %v tableLog %d: workspaces differ", what, c.norm, c.tableLog)
		}
		checkTableBound(t, what, dt)
	}
}

// checkFSERejected runs buildFSETableAsm on the invalid norm with its
// buffers against guard pages (after one, then before one), from random
// starting bytes, and fails t unless it rejects norm having written only
// what its init loop writes before failing: symbolNext[s] (the count, 1
// for -1) for each symbol s before the failing one, and the baseValue of
// the top cell for each -1 symbol among them; the header, the other cells
// and spread stay as they were.
func checkFSERejected(t testing.TB, what string, norm []int16, tableLog uint) {
	t.Helper()
	n := len(norm)
	tableSize := 1 << tableLog
	for _, before := range []bool{false, true} {
		dt := guardSlice[seqSymbol](t, 1+tableSize, before)
		gn := guardSlice[int16](t, n, before)
		copy(gn, norm)
		base := guardSlice[uint32](t, n, before)
		bits := guardSlice[uint8](t, n, before)
		wksp := guardWksp(t, before)
		var full [1 + 1<<maxFSELog]seqSymbol
		fillFSEState(full[:], wksp, fseSeed(fseTableCase{norm: norm, tableLog: tableLog}))
		copy(dt, full[:])
		wantDT := append([]seqSymbol(nil), dt...)
		wantW := *wksp
		ht := tableSize - 1
		for s, c := range norm {
			if c < -1 || (c == -1 && ht < 0) {
				break
			}
			if c == -1 {
				wantDT[1+ht].baseValue = uint32(s)
				ht--
				c = 1
			}
			wantW.symbolNext[s] = uint16(c)
		}
		if buildFSETableAsm(&dt[0], &gn[0], uint(n), &base[0], &bits[0], tableLog, wksp) {
			t.Fatalf("%s: invalid norm %v at tableLog %d accepted", what, norm, tableLog)
		}
		for i := range dt {
			if dt[i] != wantDT[i] {
				t.Fatalf("%s: norm %v tableLog %d: rejected, but cell %d is %+v, want %+v", what, norm, tableLog, i-1, dt[i], wantDT[i])
			}
		}
		if *wksp != wantW {
			t.Fatalf("%s: norm %v tableLog %d: rejected, but the workspace has other writes", what, norm, tableLog)
		}
	}
}

// fseRandomCases returns n random valid cases over every table log and
// symbol count, half with low-probability symbols.
func fseRandomCases(rng *rand.Rand, n int) []fseTableCase {
	cases := make([]fseTableCase, n)
	for i := range cases {
		maxSV := rng.IntN(maxSeq + 1)
		tableLog := uint(fse.MinTableLog + rng.IntN(maxFSELog-fse.MinTableLog+1))
		base, bits := fseCodeTables(i, maxSV)
		cases[i] = fseTableCase{randomFSENorm(rng, maxSV, tableLog, i%2 == 0), tableLog, base, bits}
	}
	return cases
}

// corpusFSECases returns the distinct FSE-compressed sequence-table
// descriptions (counts, table log, symbol type) of every compressed block
// of the corpus frames.
func corpusFSECases(t *testing.T) []fseTableCase {
	seen := map[string]bool{}
	var cases []fseTableCase
	types := []struct {
		max  uint
		base []uint32
		bits []uint8
	}{{maxLL, llBase[:], llBits[:]}, {maxOff, ofBase[:], ofBits[:]}, {maxML, mlBase[:], mlBits[:]}}
	compressedBlockHook = func(d *dctx, dst []byte, dstCap int, block []byte, streaming streamingOperation, cc bool) {
		d2 := *d
		if cc {
			d2.checkContinuity(dst, dstCap)
		}
		out := append([]byte(nil), dst[:len(dst)+dstCap]...)
		litCSize, e := d2.decodeLiteralsBlock(block, out[len(dst):], dstCap, streaming)
		if e != 0 {
			return
		}
		src := block[litCSize:]
		// C: ZSTD_decodeSeqHeaders, up to the symbol encoding types.
		ip := 0
		if len(src) < 2 {
			return
		}
		switch b := src[0]; {
		case b == 0:
			return
		case b < 0x80:
			ip = 1
		case b < 0xFF:
			ip = 2
		default:
			ip = 3
		}
		if ip >= len(src) {
			return
		}
		modes := src[ip]
		ip++
		for i, sh := range []uint{6, 4, 2} { // LL, OF, ML
			switch symbolEncodingType(modes >> sh & 3) {
			case setRLE:
				ip++
			case setCompressed:
				var norm [maxSeq + 1]int16
				maxSV := types[i].max
				if ip > len(src) {
					return
				}
				tableLog, n, err := fse.ReadNCount(norm[:], &maxSV, src[ip:])
				if err != nil || tableLog > maxFSELog {
					return
				}
				ip += n
				c := fseTableCase{append([]int16(nil), norm[:maxSV+1]...), tableLog, types[i].base, types[i].bits}
				key := fmt.Sprint(i, c.tableLog, c.norm)
				if !seen[key] {
					seen[key] = true
					cases = append(cases, c)
				}
			}
		}
	}
	defer func() { compressedBlockHook = nil }()
	for _, name := range creftest.FrameNames(t) {
		if strings.HasPrefix(name, "dict") || strings.HasPrefix(name, "multi/") || strings.HasPrefix(name, "skippable") {
			continue
		}
		f := creftest.LoadFrame(t, name)
		if f.CErr != "" || f.Dict != nil || len(f.Want) > 1<<21 {
			continue
		}
		fsi := findFrameSizeInfo(f.Data)
		if fsi.err != 0 || fsi.compressedSize != len(f.Data) || isSkippableFrame(f.Data) {
			continue
		}
		decodeFrameByBlocks(new(dctx), f.Data, len(f.Want), 0, -1, bmBlock, false)
	}
	return cases
}

// TestBuildFSETableAsmEquiv compares buildFSETableAsm with the Go body
// (checkFSETableAsm) on every distinct FSE table description of the corpus
// frames, on the default distributions, and on random valid ones at every
// table log, with and without low-probability symbols.
func TestBuildFSETableAsmEquiv(t *testing.T) {
	if !useFSETableAsm {
		t.Log("assembly off (no BMI2, or FASTZSTD_FUZZ_ASM): comparing the Go body with itself")
	}
	corpus := corpusFSECases(t)
	if len(corpus) < 1000 {
		t.Fatalf("%d corpus tables", len(corpus))
	}
	lowProb := 0
	for i, c := range corpus {
		checkFSETableAsm(t, fmt.Sprint("corpus ", i), c)
		if !validFSENorm(c.norm, c.tableLog) {
			t.Fatalf("corpus %d: invalid norm %v", i, c.norm)
		}
		for _, n := range c.norm {
			if n == -1 {
				lowProb++
				break
			}
		}
	}
	t.Logf("%d corpus tables, %d with low-probability symbols", len(corpus), lowProb)
	if lowProb == 0 || lowProb == len(corpus) {
		t.Fatal("the corpus tables do not take both spread paths")
	}
	checkFSETableAsm(t, "LL default", fseTableCase{llDefaultNorm[:], llDefaultNormLog, llBase[:], llBits[:]})
	checkFSETableAsm(t, "ML default", fseTableCase{mlDefaultNorm[:], mlDefaultNormLog, mlBase[:], mlBits[:]})
	checkFSETableAsm(t, "OF default", fseTableCase{ofDefaultNorm[:], ofDefaultNormLog, ofBase[:], ofBits[:]})
	// Edge cases: every cell a -1 symbol (no step walk, highThreshold
	// ends at -1); one symbol with every cell; a count of exactly
	// largeLimit (fastMode 0) and one below it.
	allLow := make([]int16, 32)
	for i := range allLow {
		allLow[i] = -1
	}
	checkFSETableAsm(t, "32 -1 symbols at tableLog 5", fseTableCase{allLow, 5, ofBase[:], ofBits[:]})
	for _, tl := range []uint{5, 9} {
		checkFSETableAsm(t, fmt.Sprint("one symbol at tableLog ", tl), fseTableCase{[]int16{1 << tl}, tl, llBase[:], llBits[:]})
		half := int16(1 << (tl - 1))
		checkFSETableAsm(t, fmt.Sprint("largeLimit at tableLog ", tl), fseTableCase{[]int16{half, half - 1, 1}, tl, llBase[:], llBits[:]})
		checkFSETableAsm(t, fmt.Sprint("two halves at tableLog ", tl), fseTableCase{[]int16{half, half}, tl, llBase[:], llBits[:]})
		checkFSETableAsm(t, fmt.Sprint("below largeLimit at tableLog ", tl), fseTableCase{[]int16{half - 1, half - 1, -1, 1}, tl, llBase[:], llBits[:]})
	}
	n := 20000
	if testing.Short() {
		n = 2000
	}
	for i, c := range fseRandomCases(rand.New(rand.NewPCG(20, 3)), n) {
		checkFSETableAsm(t, fmt.Sprint("random ", i), c)
	}
}

// TestBuildFSETableAsmGuard checks that buildFSETableFast leaves the table
// to Go when the arguments break buildFSETableAsm's preconditions, and that
// the assembly rejects distributions that are not valid (a count below -1,
// a sum other than 1 << tableLog, more -1 symbols than cells) without
// touching memory outside its buffers.
func TestBuildFSETableAsmGuard(t *testing.T) {
	withAsm(t, true, func() {
		if !useFSETableAsm {
			t.Skip("no BMI2")
		}
		var dt [1 + 1<<maxFSELog]seqSymbol
		var wksp buildFSETableWksp
		norm := llDefaultNorm[:]
		for _, g := range []struct {
			name     string
			dt       []seqSymbol
			norm     []int16
			maxSV1   uint
			base     []uint32
			bits     []uint8
			tableLog uint
		}{
			{"tableLog 4", dt[:], norm, maxLL + 1, llBase[:], llBits[:], 4},
			{"tableLog 10", dt[:], norm, maxLL + 1, llBase[:], llBits[:], 10},
			{"maxSV1 0", dt[:], norm, 0, llBase[:], llBits[:], 6},
			{"maxSV1 54", dt[:], make([]int16, 54), 54, make([]uint32, 54), make([]uint8, 54), 6},
			{"short norm", dt[:], norm[:maxLL], maxLL + 1, llBase[:], llBits[:], 6},
			{"short base", dt[:], norm, maxLL + 1, llBase[:maxLL], llBits[:], 6},
			{"short bits", dt[:], norm, maxLL + 1, llBase[:], llBits[:maxLL], 6},
			{"short dt", dt[:1<<6], norm, maxLL + 1, llBase[:], llBits[:], 6},
		} {
			if buildFSETableFast(g.dt, g.norm, g.maxSV1, g.base, g.bits, g.tableLog, &wksp) {
				t.Errorf("%s: the assembly ran", g.name)
			}
		}
		if !buildFSETableFast(dt[:1+1<<6], norm, maxLL+1, llBase[:], llBits[:], 6, &wksp) {
			t.Error("the assembly rejected the LL default distribution")
		}

		bad := func(name string, norm []int16, tableLog uint) {
			t.Helper()
			checkFSERejected(t, name, norm, tableLog)
		}
		over := append([]int16(nil), llDefaultNorm[:]...)
		over[0]++
		bad("sum over", over, llDefaultNormLog)
		under := append([]int16(nil), llDefaultNorm[:]...)
		under[0]--
		bad("sum under", under, llDefaultNormLog)
		below := append([]int16(nil), llDefaultNorm[:]...)
		below[1], below[2] = -2, below[2]+below[1]+2 // the sum stays 64
		bad("count -2", below, llDefaultNormLog)
		allLow := make([]int16, maxSeq+1)
		for i := range allLow {
			allLow[i] = -1
		}
		bad("53 low-probability symbols at tableLog 5", allLow, 5)
		huge := make([]int16, maxSeq+1)
		huge[0] = 32767
		bad("count 32767", huge, 5)
	})
}

// TestBuildFSETableAsmLayout checks the layouts buildFSETableAsm relies on:
// seqSymbol's fields (C's ZSTD_seqSymbol) and spread at wksp + 106, after
// symbolNext's maxSeq+1 entries (C: ZSTD_buildFSETable_body's carving).
func TestBuildFSETableAsmLayout(t *testing.T) {
	var c seqSymbol
	var w buildFSETableWksp
	for _, f := range []struct {
		name      string
		got, want uintptr
	}{
		{"nextState", unsafe.Offsetof(c.nextState), 0},
		{"nbAdditionalBits", unsafe.Offsetof(c.nbAdditionalBits), 2},
		{"nbBits", unsafe.Offsetof(c.nbBits), 3},
		{"baseValue", unsafe.Offsetof(c.baseValue), 4},
		{"sizeof(seqSymbol)", unsafe.Sizeof(c), 8},
		{"spread", unsafe.Offsetof(w.spread), 106},
		{"len(spread)", uintptr(len(w.spread)), 1<<maxFSELog + 8},
	} {
		if f.got != f.want {
			t.Errorf("%s at %d, want %d", f.name, f.got, f.want)
		}
	}
}

// TestBuildFSETableAsmRegistered checks the predicate registered with
// cpu.RegisterAsm: the builder is registered once with its switch, follows
// the detected BMI2 in SetAsmMode("on"), and is off in "nobmi2" and "off".
func TestBuildFSETableAsmRegistered(t *testing.T) {
	found := 0
	for _, r := range cpu.AsmRoutines() {
		if r.Name == "buildFSETable" {
			found++
			if r.Use != &useFSETableAsm {
				t.Error("registered with the wrong switch")
			}
		}
	}
	if found != 1 {
		t.Fatalf("found %d registrations of buildFSETable", found)
	}
	defer func() {
		if err := cpu.SetAsmMode(os.Getenv("FASTZSTD_FUZZ_ASM")); err != nil {
			t.Fatal(err)
		}
	}()
	for _, m := range []struct {
		mode string
		want bool
	}{{"nobmi2", false}, {"off", false}, {"on", true}} {
		if err := cpu.SetAsmMode(m.mode); err != nil {
			t.Fatal(err)
		}
		if m.mode == "on" {
			m.want = cpu.X86.HasBMI2
		}
		if useFSETableAsm != m.want {
			t.Errorf("mode %s: asm %v", m.mode, useFSETableAsm)
		}
	}
}

// FuzzBuildFSETableAsmEquiv builds tables from three kinds of input: the
// counts fse.ReadNCount reads from data (valid ones: checkFSETableAsm
// compares the assembly with Go on guard pages); the counts taken raw from
// data's 16-bit words (mostly invalid); and, for logSel >= 128, the
// ReadNCount counts with one count changed (kind>>2 picks it, logSel&3
// how: +1, -1, -2 with the sum kept, or -1), which are mostly near-valid.
// On the last two the assembly, on guard pages, must accept exactly the
// valid ones and then match Go, and reject the others writing only what
// checkFSERejected allows.
func FuzzBuildFSETableAsmEquiv(f *testing.F) {
	for _, s := range []struct {
		data []byte
		kind byte
	}{
		{[]byte{0x30, 0x6f, 0x9b, 0x03, 0x00, 0x00, 0x00, 0x00}, 0},
		{[]byte{0x21, 0x10, 0x8a, 0x47, 0x2c, 0x11, 0x00, 0x00}, 1},
		{[]byte{0x44, 0x29, 0x31, 0x08, 0x02, 0x40, 0x10, 0x05}, 2},
	} {
		f.Add(s.data, s.kind, byte(0))
	}
	for i, n := range [][]int16{llDefaultNorm[:], ofDefaultNorm[:], mlDefaultNorm[:]} {
		raw := make([]byte, 2*len(n))
		for j, c := range n {
			binary.LittleEndian.PutUint16(raw[2*j:], uint16(c))
		}
		f.Add(raw, byte(i), []byte{6, 5, 6}[i]) // tableLog 5 + logSel%5
	}
	for op := byte(0); op < 4; op++ {
		f.Add([]byte{0x30, 0x6f, 0x9b, 0x03, 0x00, 0x00, 0x00, 0x00}, op<<2, 128+op) // perturbed
	}
	f.Fuzz(func(t *testing.T, data []byte, kind, logSel byte) {
		maxSVs := [3]uint{maxLL, maxOff, maxML}
		k := int(kind % 3)
		base, bits := fseCodeTables(k, 0)
		var norm []int16
		var tableLog uint
		what := "raw"
		if logSel < 3 || logSel >= 128 {
			maxSV := maxSVs[k]
			var counts [maxSeq + 1]int16
			tl, _, err := fse.ReadNCount(counts[:maxSV+1], &maxSV, data)
			if err != nil || tl > maxFSELog {
				return
			}
			norm, tableLog = counts[:maxSV+1], tl
			if logSel < 3 {
				checkFSETableAsm(t, "readNCount", fseTableCase{norm, tableLog, base, bits})
				return
			}
			what = "perturbed"
			i := int(kind>>2) % len(norm)
			switch logSel & 3 {
			case 0:
				norm[i]++
			case 1:
				norm[i]--
			case 2:
				if j := (i + 1) % len(norm); j != i {
					norm[j] += norm[i] + 2
				}
				norm[i] = -2
			case 3:
				norm[i] = -1
			}
		} else {
			tableLog = uint(fse.MinTableLog) + uint(logSel)%(maxFSELog-fse.MinTableLog+1)
			n := min(len(data)/2, int(maxSVs[k])+1)
			if n == 0 {
				return
			}
			norm = make([]int16, n)
			for i := range norm {
				norm[i] = int16(binary.LittleEndian.Uint16(data[2*i:]))
			}
		}
		if validFSENorm(norm, tableLog) {
			checkFSETableAsm(t, what, fseTableCase{norm, tableLog, base, bits})
			return
		}
		if useFSETableAsm {
			checkFSERejected(t, what, norm, tableLog)
		}
	})
}

// BenchmarkBuildFSETable builds an LL table from the C harness's
// distribution (each symbol a third of what is left; no -1 symbols) at
// tableLog 6 and 9, and a tableLog 9 one with -1 symbols, with the Go body
// and the assembly.
func BenchmarkBuildFSETable(b *testing.B) {
	harness := func(tl uint) []int16 {
		var norm [maxLL + 1]int16
		rem := 1 << tl
		for s := range norm {
			n := max(rem/3, 1)
			if rem == 0 {
				n = 0
			}
			norm[s] = int16(n)
			rem -= n
		}
		norm[0] += int16(rem)
		return norm[:]
	}
	low := randomFSENorm(rand.New(rand.NewPCG(1, 2)), maxLL, 9, true)
	for _, c := range []struct {
		name string
		norm []int16
		tl   uint
	}{{"log6", harness(6), 6}, {"log9", harness(9), 9}, {"log9-lowprob", low, 9}} {
		var dt [1 + 1<<maxFSELog]seqSymbol
		var wksp buildFSETableWksp
		for _, on := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/asm=%v", c.name, on), func(b *testing.B) {
				saved := useFSETableAsm
				defer func() { useFSETableAsm = saved }()
				useFSETableAsm = on
				for i := 0; i < b.N; i++ {
					buildFSETable(dt[:], c.norm, maxLL, llBase[:], llBits[:], c.tl, &wksp)
				}
			})
		}
	}
}

package creftest

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// ldCacheEntries returns the entry files under dir.
func ldCacheEntries(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// ldBatchInput frames records for compress-batch (level, capacity, data).
func ldBatchInput(levels []int, data []byte) []byte {
	var in []byte
	for _, l := range levels {
		in = append(in, byte(l))
		in = binary.LittleEndian.AppendUint32(in, 0xFFFFFFFF)
		in = binary.LittleEndian.AppendUint32(in, uint32(len(data)))
		in = append(in, data...)
	}
	return in
}

// The cache cannot hide a bug: on a miss and on a hit, LDRun returns exactly
// what a fresh, uncached ldref run returns, for compress (all levels, all
// formats) and compress-batch; a second identical call reads the entry
// written by the first.
func TestLDCacheMatchesFresh(t *testing.T) {
	bin := LDBin(t)
	dir := t.TempDir()
	t.Setenv("FASTZSTD_LDREF_CACHE", "on")
	t.Setenv("FASTZSTD_LDREF_CACHE_DIR", dir)
	calls := 0
	check := func(name string, in []byte, args ...string) {
		t.Helper()
		fresh := run(t, bin, in, args...)
		for pass := range 2 {
			if got := LDRun(t, in, args...); !bytes.Equal(got, fresh) {
				t.Fatalf("%s %v pass %d: cached LDRun gave %d bytes, fresh ldref %d", name, args, pass, len(got), len(fresh))
			}
		}
		calls++
		if n := len(ldCacheEntries(t, dir)); n != calls {
			t.Fatalf("%s %v: %d cache entries after %d distinct calls", name, args, n, calls)
		}
	}
	for name, in := range ldInputs() {
		for _, format := range []string{LDRaw, LDGzip, LDZlib} {
			for _, level := range []int{0, 1, 4, 6, 9, 12} {
				check(name, in, "compress", "-l", strconv.Itoa(level), "-format", format)
			}
		}
		check(name, ldBatchInput([]int{1, 6, 12}, in), "compress-batch")
	}
	// A subcommand outside ldCacheable never creates an entry.
	before := len(ldCacheEntries(t, dir))
	LDRun(t, nil, "dump-params", "-l", "6")
	if n := len(ldCacheEntries(t, dir)); n != before {
		t.Fatalf("dump-params created a cache entry (%d -> %d)", before, n)
	}
}

// A corrupt entry (truncated, a flipped payload or header byte, garbage) is
// detected by its length and hash, recomputed and rewritten.
func TestLDCacheCorruptEntry(t *testing.T) {
	bin := LDBin(t)
	dir := t.TempDir()
	t.Setenv("FASTZSTD_LDREF_CACHE", "on")
	t.Setenv("FASTZSTD_LDREF_CACHE_DIR", dir)
	in := ldInputs()["rep300k"]
	args := []string{"compress", "-l", "9", "-format", LDRaw}
	fresh := run(t, bin, in, args...)
	LDRun(t, in, args...)
	files := ldCacheEntries(t, dir)
	if len(files) != 1 {
		t.Fatalf("%d entries, want 1", len(files))
	}
	good, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]byte{
		"truncated":    good[:len(good)-1],
		"payload-flip": append(append([]byte(nil), good[:len(good)-1]...), good[len(good)-1]^1),
		"length":       append(append(append([]byte(nil), good[:4]...), 0xFF), good[5:]...),
		"magic":        append([]byte("LDC0"), good[4:]...),
		"garbage":      []byte("x"),
		"empty":        {},
	} {
		if err := os.WriteFile(files[0], bad, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ldReadEntry(files[0]); err != errLDEntry {
			t.Fatalf("%s: ldReadEntry err %v, want errLDEntry", name, err)
		}
		if got := LDRun(t, in, args...); !bytes.Equal(got, fresh) {
			t.Fatalf("%s: LDRun gave %d bytes, fresh %d", name, len(got), len(fresh))
		}
		if b, err := os.ReadFile(files[0]); err != nil || !bytes.Equal(b, good) {
			t.Fatalf("%s: entry not rewritten (err %v)", name, err)
		}
	}
}

// verify mode fails when an entry differs from ldref's output (here: a
// well-formed entry holding the wrong bytes); on mode would return it, which
// is why the spot-check and verify mode exist.
func TestLDCacheVerify(t *testing.T) {
	bin := LDBin(t)
	dir := t.TempDir()
	t.Setenv("FASTZSTD_LDREF_CACHE_DIR", dir)
	in := []byte("verify verify verify verify")
	args := []string{"compress", "-l", "6", "-format", LDRaw}
	hash, err := ldBinHash(bin)
	if err != nil {
		t.Fatal(err)
	}
	key := ldCacheKey(hash, args, in)
	path := filepath.Join(dir, key[:2], key)
	if err := ldWriteEntry(path, []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	ft := &fatalRecorder{TB: t}
	t.Setenv("FASTZSTD_LDREF_CACHE", "verify")
	func() {
		defer func() { _ = recover() }()
		ldRunCached(ft, bin, in, args...)
	}()
	if !ft.failed {
		t.Fatal("verify mode accepted an entry that differs from ldref")
	}
	if !ldSpotCheck(key) { // a spot-checked key would fail here as in verify mode
		t.Setenv("FASTZSTD_LDREF_CACHE", "on")
		if got := LDRun(t, in, args...); string(got) != "wrong" {
			t.Fatalf("on mode did not use the (planted) entry: %q", got)
		}
	}
}

// fatalRecorder records Fatalf instead of stopping the test (it panics to
// unwind, the caller recovers).
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.failed = true
	f.TB.Logf("expected failure: "+format, args...)
	panic(f)
}

// FASTZSTD_LDREF_CACHE=off (and CI without an explicit setting) writes
// nothing; a directory inside the repository is refused; any other value is
// an error.
func TestLDCacheModes(t *testing.T) {
	LDBin(t)
	dir := t.TempDir()
	t.Setenv("FASTZSTD_LDREF_CACHE_DIR", dir)
	in := []byte("mode mode mode")
	for _, c := range []struct{ cache, ci, want string }{
		{"off", "", "off"}, {"0", "", "off"}, {"", "true", "off"}, {"on", "true", "on"}, {"", "", "on"},
		{"verify", "", "verify"}, {"bogus", "", "invalid"},
	} {
		t.Setenv("FASTZSTD_LDREF_CACHE", c.cache)
		t.Setenv("CI", c.ci)
		if got := ldCacheMode(); got != c.want {
			t.Fatalf("FASTZSTD_LDREF_CACHE=%q CI=%q: mode %s, want %s", c.cache, c.ci, got, c.want)
		}
	}
	t.Setenv("CI", "")
	t.Setenv("FASTZSTD_LDREF_CACHE", "off")
	LDRun(t, in, "compress", "-l", "1")
	if n := len(ldCacheEntries(t, dir)); n != 0 {
		t.Fatalf("off: %d entries", n)
	}
	t.Setenv("FASTZSTD_LDREF_CACHE", "on")
	for _, d := range []string{root(), filepath.Join(root(), "ref", "cache")} {
		t.Setenv("FASTZSTD_LDREF_CACHE_DIR", d)
		if got, why := ldCacheDir(); got != "" || why == "" {
			t.Fatalf("cache dir %s inside the repository accepted (%q)", d, got)
		}
	}
	t.Setenv("FASTZSTD_LDREF_CACHE_DIR", filepath.Join(filepath.Dir(root()), filepath.Base(root())+"-cache"))
	if got, why := ldCacheDir(); got == "" {
		t.Fatalf("sibling directory refused: %s", why)
	}
}

// The key separates the binary, every argument (including its boundaries)
// and stdin.
func TestLDCacheKey(t *testing.T) {
	base := ldCacheKey("h", []string{"compress", "-l", "1"}, []byte("x"))
	for i, k := range []string{
		ldCacheKey("h2", []string{"compress", "-l", "1"}, []byte("x")),
		ldCacheKey("h", []string{"compress", "-l", "12"}, []byte("x")),
		ldCacheKey("h", []string{"compress", "-l1"}, []byte("x")),
		ldCacheKey("h", []string{"compress", "-l", "1", "-format", "raw"}, []byte("x")),
		ldCacheKey("h", []string{"compress", "-l", "1"}, []byte("y")),
		ldCacheKey("h", []string{"compress", "-l", "1"}, nil),
	} {
		if k == base {
			t.Fatalf("key %d collides with the base key", i)
		}
	}
	if k := ldCacheKey("h", []string{"compress", "-l", "1"}, []byte("x")); k != base {
		t.Fatal("key not deterministic")
	}
}

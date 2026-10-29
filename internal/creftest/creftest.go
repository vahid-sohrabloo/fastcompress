// Package creftest drives the C reference implementation (libzstd v1.5.7,
// built as ref/cref/cref by ref/fetch.sh + make) from Go tests. cref runs as a
// separate process; there is no cgo.
package creftest

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// root returns the repository root (two directories above this file).
func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// tool returns the path to a built tool, failing the test if it is missing and
// CI is set, and skipping it otherwise.
func tool(t testing.TB, rel, how string) string {
	t.Helper()
	p := filepath.Join(root(), filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		msg := rel + " not found (" + how + ")"
		if os.Getenv("CI") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return p
}

// Bin returns the path of the cref binary.
func Bin(t testing.TB) string {
	t.Helper()
	return tool(t, "ref/cref/cref", "run ref/fetch.sh --no-corpus && make -C ref/cref")
}

func run(t testing.TB, path string, in []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v: %s", filepath.Base(path), args, err, stderr.String())
	}
	return out.Bytes()
}

// Run feeds in to `cref args...` and returns its stdout.
func Run(t testing.TB, in []byte, args ...string) []byte {
	t.Helper()
	return run(t, Bin(t), in, args...)
}

// DecodeC decompresses frame with the C zstd program (`zstd -d -c`).
func DecodeC(t testing.TB, frame []byte) []byte {
	t.Helper()
	p := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	return run(t, p, frame, "-d", "-c")
}

const (
	kib = 1 << 10
	mib = 1 << 20
)

var words = []string{
	"the", "of", "and", "compression", "zstandard", "block", "literal", "match",
	"offset", "sequence", "window", "frame", "entropy", "table", "huffman",
	"symbol", "stream", "dictionary", "checksum", "level", "bit", "state",
	"encoder", "decoder", "buffer", "length", "repeat", "history", "hash", "chain",
}

// text returns n bytes of deterministic pseudo-text (seeded word soup).
func text(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, 0, n+16)
	for len(b) < n {
		b = append(b, words[r.IntN(len(words))]...)
		switch r.IntN(12) {
		case 0:
			b = append(b, ".\n"...)
		case 1:
			b = append(b, ", "...)
		default:
			b = append(b, ' ')
		}
	}
	return b[:n]
}

func random(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 1))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func period(p, n int) []byte {
	r := rand.New(rand.NewPCG(uint64(p), 2))
	unit := make([]byte, p)
	for i := range unit {
		unit[i] = byte(r.Uint32())
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = unit[i%p]
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for ; n > 0; n /= 10 {
		s = append([]byte{byte('0' + n%10)}, s...)
	}
	return string(s)
}

// Inputs returns the deterministic input corpus for C-vs-Go comparisons: the
// synthetic set (sizes 0, 1, 16KiB±1, 128KiB±1, 256KiB±1, window-size±1,
// all-zero, single repeated byte, random, periods 2..8, text) plus the C
// repo's README/format spec (internal/creftest/testdata). Unless
// testing.Short() it also includes inputs > 1 MiB and bench/corpus/silesia/*
// when present.
func Inputs(t testing.TB) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	add := func(name string, b []byte) { m[name] = b }

	add("size0", []byte{})
	add("size1", []byte{'x'})
	for _, k := range []int{16, 128, 256} {
		for _, d := range []int{-1, 0, 1} {
			n := k*kib + d
			name := "text" + itoa(k) + "KiB"
			if d < 0 {
				name += "-1"
			} else if d > 0 {
				name += "+1"
			}
			add(name, text(uint64(n), n))
		}
	}
	// Window-size boundary: level 1 uses windowLog 19 for mid-size inputs.
	for _, d := range []int{-1, 0, 1} {
		n := 512*kib + d
		name := "win512KiB"
		if d < 0 {
			name += "-1"
		} else if d > 0 {
			name += "+1"
		}
		add(name, text(uint64(n)+7, n))
	}
	add("zeros300KiB", make([]byte, 300*kib))
	add("byteA300KiB", bytes.Repeat([]byte{'A'}, 300*kib))
	add("random200KiB", random(42, 200*kib))
	add("random-small", random(43, 100))
	for p := 2; p <= 8; p++ {
		add("period"+itoa(p), period(p, 64*kib))
	}
	for _, f := range []string{"zstd-README.md", "zstd-format.md"} {
		b, err := os.ReadFile(filepath.Join(root(), "internal", "creftest", "testdata", f))
		if err != nil {
			t.Fatalf("read testdata %s: %v", f, err)
		}
		add(f, b)
	}

	if testing.Short() {
		return m
	}
	// Window-size boundary at level 3 (windowLog 21) and other large inputs.
	for _, d := range []int{-1, 0, 1} {
		n := 2*mib + d
		name := "win2MiB"
		if d < 0 {
			name += "-1"
		} else if d > 0 {
			name += "+1"
		}
		add(name, text(uint64(n)+11, n))
	}
	add("text4MiB", text(99, 4*mib))
	add("random2MiB", random(44, 2*mib))
	ents, _ := filepath.Glob(filepath.Join(root(), "bench", "corpus", "silesia", "*"))
	sort.Strings(ents)
	for _, p := range ents {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		add("silesia/"+filepath.Base(p), b)
	}
	return m
}

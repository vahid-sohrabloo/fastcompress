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

// litRLE is 8192 random 16-byte tokens followed by 7700 of them, shuffled,
// each preceded by 'x'. At level 11 the second half's literals are mostly
// 'x', which reaches the RLE literals section.
func litRLE() []byte {
	r := rand.New(rand.NewPCG(7, 4))
	const nTok, tokLen, nRep = 8192, 16, 7700
	b := make([]byte, nTok*tokLen, nTok*tokLen+nRep*(tokLen+1))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	for _, t := range r.Perm(nTok)[:nRep] {
		b = append(b, 'x')
		b = append(b, b[t*tokLen:(t+1)*tokLen]...)
	}
	return b
}

// litLong is 70000 random bytes then 60000 copies of one byte: a single
// block whose first sequence has a literal length above 0xFFFF.
func litLong() []byte {
	b := random(45, 70000)
	return append(b, bytes.Repeat([]byte{'z'}, 60000)...)
}

// nbSeqLong is a 128 KiB single block that level 1 cuts into more than
// LONGNBSEQ (0x7F00) sequences, exercising the 3-byte nbSeq header: 9 random
// bytes, a 64-byte copy of them at offset 9 (the match that sets up the
// repcodes), then alternating 4-byte chunks that repeat the previous byte
// (offset 1) and copy from 9 bytes back (offset 9). Each chunk mismatches
// at its 5th byte, so fast's repcode loop stores one 4-byte match per chunk
// with no literals. With these exact bytes `cref seqs -level 1` reports
// 32749 sequences.
func nbSeqLong() []byte {
	const n, off = 128 * kib, 9
	r := rand.New(rand.NewPCG(0, 3))
	b := make([]byte, 0, n)
	for range off {
		b = append(b, byte(r.Uint32()))
	}
	for range 64 {
		b = append(b, b[len(b)-off])
	}
	for d := 1; len(b)+4 <= n; d ^= 1 ^ off {
		for range 4 {
			b = append(b, b[len(b)-d])
		}
	}
	for len(b) < n {
		b = append(b, 0)
	}
	return b
}

// stepFour targets the fast search's "if step <= 4" hash-table write
// (zstd_fast.c, second probe): 700 bytes of compressible noise (64 symbols)
// with R[10:18] copied to p, and the 5 bytes at p+4 copied to 320. At level
// 1 (mls 5, hashLog 11) the search runs at step 4 near 300; for these
// (seed, p) it finds the copy at p on its second probe at ip0 = p+1 and,
// only because step <= 4, also records ip1 = ip0+3 = p+4 in the hash table.
// After the match the search restarts at step 2 and visits 320, whose hash
// slot then holds p+4: a match that is absent without that write. The
// probe phase and hash collisions depend on the noise, so the (seed, p)
// pairs were chosen by checking that output changes when the condition is
// mutated to step <= 3.
func stepFour(seed uint64, p int) []byte {
	r := rand.New(rand.NewPCG(seed, 5))
	b := make([]byte, 700)
	for i := range b {
		b[i] = '0' + byte(r.Uint32()&63)
	}
	copy(b[p:p+8], b[10:18])
	copy(b[320:325], b[p+4:p+9])
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
// all-zero, single repeated byte, random, periods 2..8, text, and the
// entropy edge cases litrle, litlong and nbseqlong) plus the C
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
	add("litrle", litRLE())
	add("litlong", litLong())
	add("nbseqlong", nbSeqLong())
	for _, v := range []struct {
		seed uint64
		p    int
	}{{100, 302}, {113, 302}, {135, 303}} {
		add("step4-"+itoa(int(v.seed))+"-"+itoa(v.p), stepFour(v.seed, v.p))
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

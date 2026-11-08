// Package creftest drives the C reference implementation (libzstd v1.5.7,
// built as ref/cref/cref by ref/fetch.sh + make) from Go tests. cref runs as a
// separate process; there is no cgo.
package creftest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// RunErr is Run for a cref call that may fail: it returns cref's stdout,
// or an error with its stderr when it exits non-zero.
func RunErr(t testing.TB, in []byte, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(Bin(t), args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cref %v: %w: %s", args, err, stderr.String())
	}
	return out.Bytes(), nil
}

// DecodeC decompresses frame with the C zstd program (`zstd -d -c`).
func DecodeC(t testing.TB, frame []byte) []byte {
	t.Helper()
	p := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	return run(t, p, frame, "-d", "-c")
}

// DecodeCDict decompresses frame with the C zstd program and dictionary
// dict (`zstd -d -c -D`). It returns an error when zstd fails.
func DecodeCDict(t testing.TB, frame, dict []byte) ([]byte, error) {
	t.Helper()
	p := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	f := filepath.Join(t.TempDir(), "dict")
	if err := os.WriteFile(f, dict, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p, "-d", "-c", "-D", f)
	cmd.Stdin = bytes.NewReader(frame)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("zstd -d -D: %w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
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

// rleSeqs is 8192 random bytes from a 64-symbol alphabet without 'x', then
// 201 records of one 'x' and a 16-byte copy from 5000 bytes back (inside the
// random part, never next to an 'x'): many identical sequences (literal
// length 1, match length 16, one offset or repcode 1). Compressed with a
// flush after the first record, the frames reach RLE literals and the RLE
// literal-length and match-length table modes (levels 1 and 3) and the RLE
// offset mode (level 19).
func rleSeqs() []byte {
	r := rand.New(rand.NewPCG(11, 6))
	b := make([]byte, 8192, 8192+201*17)
	for i := range b {
		b[i] = '0' + byte(r.IntN(64)) // '0'..'o'
	}
	for range 201 {
		p := len(b) + 1 - 5000
		b = append(b, 'x')
		b = append(b, b[p:p+16]...)
	}
	return b
}

// deBruijn is the de Bruijn sequence B(16, 3) over 'a'..'p' (4096 bytes):
// every 3-byte string occurs at most once, so a compressor finds no match
// and the block is compressed literals with no sequences.
func deBruijn() []byte {
	const k, n = 16, 3
	a := make([]int, k*n)
	var b []byte
	var db func(t, p int)
	db = func(t, p int) {
		if t > n {
			if n%p == 0 {
				for _, v := range a[1 : p+1] {
					b = append(b, 'a'+byte(v))
				}
			}
			return
		}
		a[t] = a[t-p]
		db(t+1, p)
		for j := a[t-p] + 1; j < k; j++ {
			a[t] = j
			db(t+1, t)
		}
	}
	db(1, 1)
	return b
}

// Inputs returns the deterministic input corpus for C-vs-Go comparisons: the
// synthetic set (sizes 0, 1, 16KiB±1, 128KiB±1, 256KiB±1, window-size±1,
// all-zero, single repeated byte, random, periods 2..8, text, and the
// entropy edge cases litrle, litlong and nbseqlong, and the step4-* inputs)
// plus the C repo's README/format spec (internal/creftest/testdata). Unless
// testing.Short() it also includes inputs > 1 MiB and bench/corpus/silesia/*
// when present.
func Inputs(t testing.TB) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	srcs, _ := inputSources(t)
	for name, gen := range srcs {
		b, err := gen()
		if err != nil {
			t.Fatal(err)
		}
		m[name] = b
	}
	return m
}

// source produces one input on demand.
type source func() ([]byte, error)

func fixed(b []byte) source { return func() ([]byte, error) { return b, nil } }

// inputSources returns Inputs as generators, so the decode corpus can
// rebuild one input at a time instead of holding them all, and the names of
// the inputs over 1 MiB (only without testing.Short()). Only the large
// inputs are deferred; the small ones are built here.
func inputSources(t testing.TB) (m map[string]source, large map[string]bool) {
	t.Helper()
	m, large = map[string]source{}, map[string]bool{}
	add := func(name string, b []byte) { m[name] = fixed(b) }

	add("size0", []byte{})
	add("size1", []byte{'x'})
	for _, k := range []int{16, 128, 256} {
		for _, d := range []int{-1, 0, 1} {
			n := k*kib + d
			name := "text" + strconv.Itoa(k) + "KiB"
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
		add("period"+strconv.Itoa(p), period(p, 64*kib))
	}
	add("litrle", litRLE())
	add("litlong", litLong())
	add("nbseqlong", nbSeqLong())
	for _, v := range []struct {
		seed uint64
		p    int
	}{{100, 302}, {113, 302}, {135, 303}} {
		add("step4-"+strconv.Itoa(int(v.seed))+"-"+strconv.Itoa(v.p), stepFour(v.seed, v.p))
	}
	for _, f := range []string{"zstd-README.md", "zstd-format.md"} {
		b, err := os.ReadFile(filepath.Join(root(), "internal", "creftest", "testdata", f))
		if err != nil {
			t.Fatalf("read testdata %s: %v", f, err)
		}
		add(f, b)
	}

	if testing.Short() {
		return m, large
	}
	small := map[string]bool{}
	for k := range m {
		small[k] = true
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
		m[name] = func() ([]byte, error) { return text(uint64(n)+11, n), nil }
	}
	m["text4MiB"] = func() ([]byte, error) { return text(99, 4*mib), nil }
	m["random2MiB"] = func() ([]byte, error) { return random(44, 2*mib), nil }
	ents, _ := filepath.Glob(filepath.Join(root(), "bench", "corpus", "silesia", "*"))
	for _, p := range ents {
		m["silesia/"+filepath.Base(p)] = func() ([]byte, error) { return os.ReadFile(p) }
	}
	for k := range m {
		if !small[k] {
			large[k] = true
		}
	}
	return m, large
}

// CError is a libzstd error reported by a cref decoder mode: its
// ZSTD_getErrorCode value and ZSTD_getErrorName text. Code -1 with Name
// "incomplete" is dstream's input ending inside a frame (C returns no
// error code for that; ZSTD_decompressStream just still wants input).
type CError struct {
	Code int
	Name string
}

func (e *CError) Error() string { return "zstd error " + strconv.Itoa(e.Code) + ": " + e.Name }

// runDec runs `cref mode args...` on frame and returns its stdout, plus the
// parsed error when cref reports one (exit 1 with an ERR or INCOMPLETE line).
// Any other failure fails the test.
func runDec(t testing.TB, mode string, frame []byte, args ...string) ([]byte, *CError) {
	t.Helper()
	args = append([]string{mode}, args...)
	cmd := exec.Command(Bin(t), args...)
	cmd.Stdin = bytes.NewReader(frame)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), nil
	}
	msg := strings.TrimSuffix(stderr.String(), "\n")
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		if rest, ok := strings.CutPrefix(msg, "ERR "); ok {
			code, name, _ := strings.Cut(rest, " ")
			if n, err := strconv.Atoi(code); err == nil {
				return out.Bytes(), &CError{Code: n, Name: name}
			}
		}
		if strings.HasPrefix(msg, "INCOMPLETE ") {
			return out.Bytes(), &CError{Code: -1, Name: "incomplete"}
		}
	}
	t.Fatalf("cref %v: %v: %s", args, err, msg)
	return nil, nil
}

// DecodeCRef decompresses frame (any number of frames) with `cref
// decompress args...` (ZSTD_decompressDCtx). On a libzstd error it returns
// the error and no output. Empty input succeeds with empty output; a
// truncated frame (down to 1 byte) fails with srcSize_wrong (72), or
// checksum_wrong (22) when the cut is in or right before the checksum, where
// DStreamCRef reports "incomplete" instead.
func DecodeCRef(t testing.TB, frame []byte, args ...string) ([]byte, *CError) {
	t.Helper()
	return runDec(t, "decompress", frame, args...)
}

// DStreamCRef decompresses frame with `cref dstream args...`
// (ZSTD_decompressStream; args must give -chunks or -chunk-size, and
// -out-size). It returns every byte produced, including those produced before
// an error. Empty input succeeds with empty output; input ending inside a
// frame returns CError{-1, "incomplete"} (DecodeCRef: srcSize_wrong).
func DStreamCRef(t testing.TB, frame []byte, args ...string) ([]byte, *CError) {
	t.Helper()
	return runDec(t, "dstream", frame, args...)
}

// FinalizeDictCRef runs `cref finalize-dict` (ZDICT_finalizeDictionary with
// a capacity of len(content) + 64 KiB and notification level 0) on content
// and samples at C level level with dictionary ID id. On a libzstd error it
// returns the error and no dictionary.
func FinalizeDictCRef(t testing.TB, content []byte, samples [][]byte, level int, id uint32) ([]byte, *CError) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "content")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sizes := make([]string, len(samples))
	for i, s := range samples {
		sizes[i] = strconv.Itoa(len(s))
	}
	out, cerr := runDec(t, "finalize-dict", slices.Concat(samples...), "-level", strconv.Itoa(level),
		"-dict-id", strconv.FormatUint(uint64(id), 10), "-content", p, "-sizes", strings.Join(sizes, ","))
	if cerr != nil {
		return nil, cerr
	}
	return out, nil
}

// TrainCRef runs `cref mode -max-dict maxDict -sizes ... args...` (mode is
// train, train-cover or train-fastcover) on the samples. It returns the
// dictionary and the key=value fields of the PARAMS line (nil for train).
// On a libzstd error it returns the error and no dictionary; when cref dies
// from a signal (C's undefined behaviour on some degenerate inputs) the
// error has Code -2.
func TrainCRef(t testing.TB, mode string, samples [][]byte, maxDict int, args ...string) ([]byte, map[string]string, *CError) {
	t.Helper()
	sizes := make([]string, len(samples))
	for i, s := range samples {
		sizes[i] = strconv.Itoa(len(s))
	}
	args = append([]string{mode, "-max-dict", strconv.Itoa(maxDict), "-sizes", strings.Join(sizes, ",")}, args...)
	cmd := exec.Command(Bin(t), args...)
	cmd.Stdin = bytes.NewReader(slices.Concat(samples...))
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := strings.TrimSuffix(stderr.String(), "\n")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && !ee.Exited() {
			return nil, nil, &CError{Code: -2, Name: "crash: " + ee.String()}
		}
		if rest, ok := strings.CutPrefix(msg, "ERR "); ok && errors.As(err, &ee) && ee.ExitCode() == 1 {
			code, name, _ := strings.Cut(rest, " ")
			if n, err := strconv.Atoi(code); err == nil {
				return nil, nil, &CError{Code: n, Name: name}
			}
		}
		t.Fatalf("cref %s: %v: %s", mode, err, msg)
	}
	var params map[string]string
	if rest, ok := strings.CutPrefix(msg, "PARAMS "); ok {
		params = map[string]string{}
		for _, f := range strings.Fields(rest) {
			k, v, _ := strings.Cut(f, "=")
			params[k] = v
		}
	} else if mode != "train" {
		t.Fatalf("cref %s: no PARAMS line: %q", mode, msg)
	}
	return out.Bytes(), params, nil
}

// ZstdCLI returns the path of the C zstd program built from ref/zstd.
func ZstdCLI(t testing.TB) string {
	t.Helper()
	return tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
}

// FHeaderCRef returns the key=value lines of `cref fheader` on in.
func FHeaderCRef(t testing.TB, in []byte) map[string]string {
	t.Helper()
	return keyValues(t, "fheader", in)
}

// DictInfoCRef returns the key=value lines of `cref dictinfo` on the
// dictionary dict (ZSTD_getDictID_fromDict, ZSTD_createDDict_advanced and
// ZSTD_loadDEntropy).
func DictInfoCRef(t testing.TB, dict []byte) map[string]string {
	t.Helper()
	return keyValues(t, "dictinfo", dict)
}

func keyValues(t testing.TB, mode string, in []byte) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(Run(t, in, mode))), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			t.Fatalf("cref %s: bad line %q", mode, l)
		}
		m[k] = v
	}
	return m
}

// Frame is one decode corpus entry. Data is the compressed input (one or
// more frames, possibly with skippable frames), Want what it decodes to.
// Dict is the dictionary the decoder is given (nil: none), RawDict whether
// that dictionary is raw content, and Args the extra cref
// decompress/dstream arguments that load it (-dict FILE [-raw-dict 1]).
// CErr, when not empty, is the ZSTD_getErrorName text C fails with (both
// decompress and dstream); Want is then empty.
type Frame struct {
	Data, Want, Dict []byte
	RawDict          bool
	Args             []string
	CErr             string
}

// corpusEntry is a decode corpus entry's index record: where its bytes are,
// never the bytes themselves (LoadFrame reads them).
type corpusEntry struct {
	file    string   // cache file of a generated frame; "" for a concatenation
	src     source   // the generator's input: Want, unless noWant
	noWant  bool     // Want is empty (skippable frame, C error)
	sum     [32]byte // sha256 of Want, checked by LoadFrame
	parts   []string // a concatenation of these entries
	hidden  bool     // a part only, not listed by FrameNames
	dict    []byte
	rawDict bool
	args    []string
	cerr    string
}

var corpus struct {
	once sync.Once
	m    map[string]*corpusEntry
	ok   bool
}

func corpusIndex(t testing.TB) map[string]*corpusEntry {
	t.Helper()
	bin := Bin(t)
	cli := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	corpus.once.Do(func() {
		corpus.m = buildCorpus(t, bin, cli)
		corpus.ok = true
	})
	if !corpus.ok {
		t.Fatal("decode corpus: building it failed in an earlier test")
	}
	return corpus.m
}

// FrameNames returns the sorted names of the decode corpus entries (see
// buildCorpus). The corpus is built (or found in its cache) once per test
// binary; only its index stays in memory.
func FrameNames(t testing.TB) []string {
	t.Helper()
	var names []string
	for k, e := range corpusIndex(t) {
		if !e.hidden {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

// LoadFrame reads one decode corpus entry: Data from the cache, Want
// rebuilt from its source input and checked against the hash recorded when
// the frame was generated.
func LoadFrame(t testing.TB, name string) Frame {
	t.Helper()
	e, ok := corpusIndex(t)[name]
	if !ok {
		t.Fatalf("decode corpus: no entry %s", name)
	}
	f := Frame{Dict: e.dict, RawDict: e.rawDict, Args: e.args, CErr: e.cerr}
	if e.parts != nil {
		f.Want = []byte{}
		for _, p := range e.parts {
			g := LoadFrame(t, p)
			f.Data = append(f.Data, g.Data...)
			f.Want = append(f.Want, g.Want...)
		}
		return f
	}
	f.Data = mustRead(t, e.file)
	if e.noWant {
		return f
	}
	w, err := e.src()
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(w) != e.sum {
		t.Fatalf("decode corpus %s: rebuilt input differs from the one the frame was made from", name)
	}
	f.Want = w
	return f
}

// Frames loads the whole decode corpus into memory. That is fine with
// testing.Short(); otherwise (silesia) prefer FrameNames and LoadFrame.
func Frames(t testing.TB) map[string]Frame {
	t.Helper()
	m := map[string]Frame{}
	for _, name := range FrameNames(t) {
		m[name] = LoadFrame(t, name)
	}
	return m
}

// mustRead reads a file, failing the test on error.
func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildCorpus builds the decode corpus index with cref and the v1.5.7 zstd
// CLI:
//
//   - compress/<input>/L<l>/c<0|1>/p<0|1>: every Inputs entry at levels
//     -5, 1, 3, 7, 11, 19, checksum 0/1, pledged size 0/1 (cref compress).
//     This includes all-zero input (RLE blocks), random input (raw blocks,
//     raw literals) and litrle (RLE literals). Inputs over 1 MiB (only
//     without testing.Short()) get levels 1, 3, 19 with c0/p1 and c1/p0.
//   - stream/...: cref stream frames with flush points (short blocks, and
//     the empty last block after a flush).
//   - window/<input>/L<l>/W<10|17>: -window-log 10 and 17 frames.
//   - dict/<dict>/<input>/L<l>/c<0|1>: frames compressed with train.dict,
//     train-12k.dict and raw-content dickens-32k.raw (zstd/testdata).
//   - dict-err/...: a train.dict frame decoded without a dictionary, and
//     with train-12k.dict (dictID mismatch): C fails with dictionary_wrong.
//   - multi/...: concatenations of frames.
//   - skippable/...: skippable frames before, between and after frames, and
//     skippable-only input.
//   - seed/...: small frames with RLE literals, RLE sequence table modes
//     (rleSeqs) and a compressed block without sequences (deBruijn).
//   - cli/...: `zstd -T4 -19 -B512KiB` (zstdmt job boundaries), `zstd
//     --long=27 -19` (stdin: the 128 MiB window stays in the header) and
//     `zstd --rsyncable -T2` frames.
//
// Only compressed frames are cached, under os.TempDir()/fastzstd-frames-<hash
// of the cref and zstd binaries>, in files named after the entry and
// cacheKey (tool, arguments, dictionary contents, input), so changed inputs
// or dictionaries regenerate. Creating a
// new hash directory deletes the other fastzstd-frames-* directories. Inputs
// are generated one job at a time and dropped, so memory stays bounded by
// the parallel jobs.
func buildCorpus(t testing.TB, bin, cli string) map[string]*corpusEntry {
	t.Helper()
	dir := frameCacheDir(t, bin, cli)

	type job struct {
		name, tool string
		args       []string
		e          *corpusEntry
	}
	m := map[string]*corpusEntry{}
	var jobs []job
	add := func(name, tool string, src source, e corpusEntry, args ...string) {
		e.src = src
		m[name] = &e
		jobs = append(jobs, job{name, tool, args, &e})
	}
	cref := func(name string, src source, e corpusEntry, args ...string) { add(name, bin, src, e, args...) }
	zcli := func(name string, src source, args ...string) {
		add(name, cli, src, corpusEntry{}, append(args, "-q", "-c")...)
	}

	inputs, large := inputSources(t)
	in := func(name string) source {
		s, ok := inputs[name]
		if !ok {
			t.Fatalf("decode corpus: no input %s", name)
		}
		return s
	}
	for name, s := range inputs {
		type set struct{ l, c, p string }
		var sets []set
		if large[name] {
			for _, l := range []string{"1", "3", "19"} {
				sets = append(sets, set{l, "0", "1"}, set{l, "1", "0"})
			}
		} else {
			for _, l := range []string{"-5", "1", "3", "7", "11", "19"} {
				for _, c := range []string{"0", "1"} {
					for _, p := range []string{"0", "1"} {
						sets = append(sets, set{l, c, p})
					}
				}
			}
		}
		for _, v := range sets {
			cref("compress/"+name+"/L"+v.l+"/c"+v.c+"/p"+v.p, s, corpusEntry{},
				"compress", "-level", v.l, "-checksum", v.c, "-pledged", v.p)
		}
	}

	for _, name := range []string{"size0", "text128KiB+1", "zstd-format.md", "litrle"} {
		b, err := in(name)()
		if err != nil {
			t.Fatal(err)
		}
		// Odd cut points with a flush after each of the first four chunks
		// (two of them back to back, the second flushing nothing).
		var cs []string
		pos := 0
		for _, sz := range []int{1000, 0, 1, 5000, 30000} {
			sz = min(sz, len(b)-pos)
			cs = append(cs, strconv.Itoa(sz))
			pos += sz
		}
		cs = append(cs, strconv.Itoa(len(b)-pos))
		cref("stream/"+name+"/cuts", fixed(b), corpusEntry{}, "stream", "-level", "3", "-checksum", "1",
			"-chunks", strings.Join(cs, ","), "-flush-after", "0,1,2,3")
		// 4 KiB chunks, each flushed: short blocks; then an empty last block.
		cs, fl := nil, []string{}
		for i := 0; i*4096 < len(b); i++ {
			cs = append(cs, strconv.Itoa(min(4096, len(b)-i*4096)))
			fl = append(fl, strconv.Itoa(i))
		}
		if len(cs) == 0 {
			cs, fl = []string{"0"}, []string{"0"}
		}
		cref("stream/"+name+"/flush4k", fixed(b), corpusEntry{}, "stream", "-level", "1",
			"-chunks", strings.Join(cs, ","), "-flush-after", strings.Join(fl, ","))
	}

	for _, name := range []string{"text256KiB", "zstd-format.md", "litrle"} {
		for _, l := range []string{"3", "19"} {
			for _, w := range []string{"10", "17"} {
				cref("window/"+name+"/L"+l+"/W"+w, in(name), corpusEntry{},
					"compress", "-level", l, "-window-log", w)
			}
		}
	}

	dictEntry := func(file string, raw bool) corpusEntry {
		p := filepath.Join(root(), "zstd", "testdata", file)
		e := corpusEntry{dict: mustRead(t, p), rawDict: raw, args: []string{"-dict", p}}
		if raw {
			e.args = append(e.args, "-raw-dict", "1")
		}
		return e
	}
	for _, d := range []struct {
		file string
		raw  bool
	}{{"train.dict", false}, {"train-12k.dict", false}, {"dickens-32k.raw", true}} {
		e := dictEntry(d.file, d.raw)
		for _, name := range []string{"size0", "size1", "random-small", "text16KiB", "zstd-README.md"} {
			for _, l := range []string{"1", "3", "19"} {
				for _, c := range []string{"0", "1"} {
					cref("dict/"+d.file+"/"+name+"/L"+l+"/c"+c, in(name), e,
						append([]string{"compress", "-level", l, "-checksum", c}, e.args...)...)
				}
			}
		}
	}
	train, train12k := dictEntry("train.dict", false), dictEntry("train-12k.dict", false)
	trainArgs := append([]string{"compress", "-level", "3"}, train.args...)
	cref("dict-err/missing/train.dict/zstd-README.md", in("zstd-README.md"),
		corpusEntry{noWant: true, cerr: "Dictionary mismatch"}, trainArgs...)
	train12k.noWant, train12k.cerr = true, "Dictionary mismatch"
	cref("dict-err/mismatch/train.dict-vs-train-12k.dict/zstd-README.md", in("zstd-README.md"), train12k, trainArgs...)

	for k, v := range map[string]struct {
		variant string
		payload []byte
	}{
		"s0":     {"0", []byte("a skippable frame payload")},
		"s7":     {"7", []byte{}},
		"s15":    {"15", random(46, 1000)},
		"s15big": {"15", random(47, 70000)},
	} {
		cref("skippable-frame/"+k, fixed(v.payload), corpusEntry{noWant: true, hidden: true}, "skippable", "-variant", v.variant)
	}

	// Small frames for the decoder seeds' coverage: RLE literals and RLE
	// sequence table modes, and a compressed block with no sequences.
	rs := rleSeqs()
	for _, l := range []string{"1", "3", "19"} {
		cref("seed/rle-seqs/L"+l, fixed(rs), corpusEntry{}, "stream", "-level", l, "-checksum", "1",
			"-chunks", "8209,"+strconv.Itoa(len(rs)-8209), "-flush-after", "0")
		cref("seed/no-seqs/L"+l, fixed(deBruijn()), corpusEntry{}, "compress", "-level", l)
	}

	mt := source(func() ([]byte, error) { return text(1234, mib), nil })
	zcli("cli/T4-19-B512KiB/text1MiB", mt, "-T4", "-19", "-B512KiB")
	zcli("cli/rsyncable/text1MiB", mt, "--rsyncable", "-T2", "-3")
	for _, name := range []string{"text256KiB", "zstd-format.md"} {
		zcli("cli/long27-19/"+name, in(name), "--long=27", "-19")
	}
	if !testing.Short() {
		zcli("cli/T4-19/text4MiB", in("text4MiB"), "-T4", "-19")
		zcli("cli/rsyncable/text4MiB", in("text4MiB"), "--rsyncable", "-T2", "-3")
		zcli("cli/long27-19/text4MiB", in("text4MiB"), "--long=27", "-19")
	}

	// Run the jobs in parallel, through the cache, keeping no bytes.
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := j.e.src()
			if err != nil {
				errs[i] = err
				return
			}
			if !j.e.noWant {
				j.e.sum = sha256.Sum256(b)
			}
			key, err := cacheKey(j.tool, j.args, b)
			if err != nil {
				errs[i] = err
				return
			}
			j.e.file = filepath.Join(dir, strings.ReplaceAll(j.name, "/", "__")+"-"+key)
			if _, err := os.Stat(j.e.file); err == nil {
				return
			}
			out, err := runCmd(j.tool, b, j.args...)
			if err == nil {
				err = writeAtomic(dir, j.e.file, out)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, j := range jobs {
		if errs[i] != nil {
			t.Fatalf("decode corpus %s: %v", j.name, errs[i])
		}
	}

	cat := func(name string, parts ...string) {
		for _, p := range parts {
			if m[p] == nil {
				t.Fatalf("decode corpus %s: no part %s", name, p)
			}
		}
		m[name] = &corpusEntry{parts: parts}
	}
	a := "compress/text16KiB/L3/c1/p1"
	b := "compress/zstd-README.md/L1/c0/p0"
	cat("multi/3", "compress/text16KiB/L1/c1/p1", "compress/zstd-README.md/L19/c0/p1", "compress/size0/L3/c0/p0")
	cat("multi/zeros+random", "compress/zeros300KiB/L1/c0/p1", "compress/random-small/L7/c1/p0")
	cat("multi/stream+compress", "stream/text128KiB+1/flush4k", "compress/litrle/L11/c1/p1")
	cat("skippable/only", "skippable-frame/s0")
	cat("skippable/only-multi", "skippable-frame/s0", "skippable-frame/s7", "skippable-frame/s15big")
	cat("skippable/before", "skippable-frame/s15", a)
	cat("skippable/between", a, "skippable-frame/s0", b)
	cat("skippable/after", a, "skippable-frame/s7")
	cat("skippable/all", "skippable-frame/s15", a, "skippable-frame/s7", b, "skippable-frame/s15big")
	return m
}

// frameCacheDir returns the frame cache directory for the cref and zstd
// binaries bin and cli, os.TempDir()/fastzstd-frames-<hash of both>,
// creating it (and then deleting the other fastzstd-frames-* directories)
// when it does not exist.
func frameCacheDir(t testing.TB, bin, cli string) string {
	t.Helper()
	h := sha256.New()
	h.Write(mustRead(t, bin))
	h.Write(mustRead(t, cli))
	const prefix = "fastzstd-frames-"
	dir := filepath.Join(os.TempDir(), prefix+hex.EncodeToString(h.Sum(nil))[:16])
	if err := os.Mkdir(dir, 0o755); err == nil {
		ents, _ := os.ReadDir(os.TempDir())
		for _, e := range ents {
			if p := filepath.Join(os.TempDir(), e.Name()); strings.HasPrefix(e.Name(), prefix) && p != dir {
				os.RemoveAll(p)
			}
		}
	} else if !os.IsExist(err) {
		t.Fatal(err)
	}
	return dir
}

// CachedCLI returns `zstd args... -q -c` (the v1.5.7 CLI) on in, cached
// under name in the decode corpus's cache directory (keyed like corpus
// frames), for frames too slow to generate on every run.
func CachedCLI(t testing.TB, name string, in []byte, args ...string) []byte {
	t.Helper()
	bin := Bin(t)
	cli := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	dir := frameCacheDir(t, bin, cli)
	args = slices.Concat(args, []string{"-q", "-c"})
	key, err := cacheKey(cli, args, in)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, strings.ReplaceAll(name, "/", "__")+"-"+key)
	if b, err := os.ReadFile(p); err == nil {
		return b
	}
	out, err := runCmd(cli, in, args...)
	if err == nil {
		err = writeAtomic(dir, p, out)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// cacheKey names a generated frame: a hash of the tool's name, its
// arguments, the contents of every file an argument names (-dict, -content:
// a regenerated dictionary at the same path is a new key) and the input.
func cacheKey(tool string, args []string, in []byte) (string, error) {
	k := sha256.New()
	k.Write([]byte(filepath.Base(tool) + "\x00" + strings.Join(args, "\x00") + "\x00"))
	for i := 1; i < len(args); i++ {
		if args[i-1] == "-dict" || args[i-1] == "-content" {
			b, err := os.ReadFile(args[i])
			if err != nil {
				return "", err
			}
			k.Write(b)
		}
	}
	k.Write(in)
	return hex.EncodeToString(k.Sum(nil))[:12], nil
}

// runCmd runs path with args and stdin in, returning stdout.
func runCmd(path string, in []byte, args ...string) ([]byte, error) {
	cmd := exec.Command(path, args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", filepath.Base(path), args, err, stderr.String())
	}
	return out.Bytes(), nil
}

// writeAtomic writes b to p through a temporary file in dir, so concurrent
// test binaries never see a partial file.
func writeAtomic(dir, p string, b []byte) error {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

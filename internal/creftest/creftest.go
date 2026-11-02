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
// the error and no output.
func DecodeCRef(t testing.TB, frame []byte, args ...string) ([]byte, *CError) {
	t.Helper()
	return runDec(t, "decompress", frame, args...)
}

// DStreamCRef decompresses frame with `cref dstream args...`
// (ZSTD_decompressStream; args must give -chunks or -chunk-size, and
// -out-size). It returns every byte produced, including those produced before
// an error.
func DStreamCRef(t testing.TB, frame []byte, args ...string) ([]byte, *CError) {
	t.Helper()
	return runDec(t, "dstream", frame, args...)
}

// FHeaderCRef returns the key=value lines of `cref fheader` on in.
func FHeaderCRef(t testing.TB, in []byte) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(Run(t, in, "fheader"))), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			t.Fatalf("cref fheader: bad line %q", l)
		}
		m[k] = v
	}
	return m
}

// Frame is one decode corpus entry. Data is the compressed input (one or
// more frames, possibly with skippable frames), Want what it decodes to.
// Dict is the dictionary it needs (nil: none), RawDict whether that
// dictionary is raw content, and Args the extra cref decompress/dstream
// arguments that load it (-dict FILE [-raw-dict 1]).
type Frame struct {
	Data, Want, Dict []byte
	RawDict          bool
	Args             []string
}

var frames struct {
	once sync.Once
	m    map[string]Frame
	ok   bool
}

// Frames returns the decode corpus (see buildFrames), built once per test
// binary. Callers must not modify the map or the slices.
func Frames(t testing.TB) map[string]Frame {
	t.Helper()
	bin := Bin(t)
	cli := tool(t, "ref/zstd/programs/zstd", "run ref/fetch.sh")
	frames.once.Do(func() {
		frames.m = buildFrames(t, bin, cli)
		frames.ok = true
	})
	if !frames.ok {
		t.Fatal("decode corpus: building it failed in an earlier test")
	}
	return frames.m
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

// buildFrames builds the decode corpus with cref and the v1.5.7 zstd CLI:
//
//   - compress/<input>/L<l>/c<0|1>/p<0|1>: every Inputs entry at levels
//     -5, 1, 3, 7, 11, 19, checksum 0/1, pledged size 0/1 (cref compress).
//     This includes all-zero input (RLE blocks), random input (raw blocks,
//     raw literals) and litrle (RLE literals).
//   - stream/...: cref stream frames with flush points (short blocks, and
//     the empty last block after a flush).
//   - window/<input>/L<l>/W<10|17>: -window-log 10 and 17 frames.
//   - dict/<dict>/<input>/L<l>/c<0|1>: frames compressed with train.dict,
//     train-12k.dict and raw-content dickens-32k.raw (zstd/testdata).
//   - multi/...: concatenations of frames.
//   - skippable/...: skippable frames before, between and after frames, and
//     skippable-only input.
//   - seed/...: small frames with RLE literals, RLE sequence table modes
//     (rleSeqs) and a compressed block without sequences (deBruijn).
//   - cli/...: `zstd -T4 -19 -B512KiB` (zstdmt job boundaries), `zstd
//     --long=27 -19` (stdin: the 128 MiB window stays in the header) and
//     `zstd --rsyncable -T2` frames.
//
// Every generated frame is cached under os.TempDir()/fastzstd-frames-<hash
// of the cref and zstd binaries>, in a file named after the entry and a hash
// of its tool, arguments and input, so changed inputs regenerate.
// Inputs over 1 MiB are included only when !testing.Short().
func buildFrames(t testing.TB, bin, cli string) map[string]Frame {
	t.Helper()
	h := sha256.New()
	h.Write(mustRead(t, bin))
	h.Write(mustRead(t, cli))
	dir := filepath.Join(os.TempDir(), "fastzstd-frames-"+hex.EncodeToString(h.Sum(nil))[:16])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	type job struct {
		name, tool string
		in         []byte
		args       []string
		f          Frame
	}
	var jobs []job
	cref := func(name string, in []byte, f Frame, args ...string) {
		f.Want = in
		jobs = append(jobs, job{name, bin, in, args, f})
	}
	zcli := func(name string, in []byte, args ...string) {
		jobs = append(jobs, job{name, cli, in, append(args, "-q", "-c"), Frame{Want: in}})
	}

	inputs := Inputs(t)
	names := make([]string, 0, len(inputs))
	for k := range inputs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, l := range []string{"-5", "1", "3", "7", "11", "19"} {
			for _, c := range []string{"0", "1"} {
				for _, p := range []string{"0", "1"} {
					cref("compress/"+name+"/L"+l+"/c"+c+"/p"+p, inputs[name], Frame{},
						"compress", "-level", l, "-checksum", c, "-pledged", p)
				}
			}
		}
	}

	for _, name := range []string{"size0", "text128KiB+1", "zstd-format.md", "litrle"} {
		in := inputs[name]
		// Odd cut points with a flush after each of the first four chunks
		// (two of them back to back, the second flushing nothing).
		var cs []string
		pos := 0
		for _, sz := range []int{1000, 0, 1, 5000, 30000} {
			sz = min(sz, len(in)-pos)
			cs = append(cs, itoa(sz))
			pos += sz
		}
		cs = append(cs, itoa(len(in)-pos))
		cref("stream/"+name+"/cuts", in, Frame{}, "stream", "-level", "3", "-checksum", "1",
			"-chunks", strings.Join(cs, ","), "-flush-after", "0,1,2,3")
		// 4 KiB chunks, each flushed: short blocks; then an empty last block.
		cs, fl := nil, []string{}
		for i := 0; i*4096 < len(in); i++ {
			cs = append(cs, itoa(min(4096, len(in)-i*4096)))
			fl = append(fl, itoa(i))
		}
		if len(cs) == 0 {
			cs, fl = []string{"0"}, []string{"0"}
		}
		cref("stream/"+name+"/flush4k", in, Frame{}, "stream", "-level", "1",
			"-chunks", strings.Join(cs, ","), "-flush-after", strings.Join(fl, ","))
	}

	for _, name := range []string{"text256KiB", "zstd-format.md", "litrle"} {
		for _, l := range []string{"3", "19"} {
			for _, w := range []string{"10", "17"} {
				cref("window/"+name+"/L"+l+"/W"+w, inputs[name], Frame{},
					"compress", "-level", l, "-window-log", w)
			}
		}
	}

	for _, d := range []struct {
		file string
		raw  bool
	}{{"train.dict", false}, {"train-12k.dict", false}, {"dickens-32k.raw", true}} {
		p := filepath.Join(root(), "zstd", "testdata", d.file)
		f := Frame{Dict: mustRead(t, p), RawDict: d.raw, Args: []string{"-dict", p}}
		if d.raw {
			f.Args = append(f.Args, "-raw-dict", "1")
		}
		for _, name := range []string{"size0", "size1", "random-small", "text16KiB", "zstd-README.md"} {
			for _, l := range []string{"1", "3", "19"} {
				for _, c := range []string{"0", "1"} {
					cref("dict/"+d.file+"/"+name+"/L"+l+"/c"+c, inputs[name], f,
						append([]string{"compress", "-level", l, "-checksum", c}, f.Args...)...)
				}
			}
		}
	}

	skip := map[string][]byte{
		"s0":     []byte("a skippable frame payload"),
		"s7":     {},
		"s15":    random(46, 1000),
		"s15big": random(47, 70000),
	}
	for _, k := range []string{"s0", "s7", "s15", "s15big"} {
		v := strings.TrimSuffix(strings.TrimPrefix(k, "s"), "big")
		jobs = append(jobs, job{"skippable-frame/" + k, bin, skip[k], []string{"skippable", "-variant", v}, Frame{}})
	}

	// Small frames for the decoder seeds' coverage: RLE literals and RLE
	// sequence table modes, and a compressed block with no sequences.
	rs := rleSeqs()
	for _, l := range []string{"1", "3", "19"} {
		cref("seed/rle-seqs/L"+l, rs, Frame{}, "stream", "-level", l, "-checksum", "1",
			"-chunks", "8209,"+itoa(len(rs)-8209), "-flush-after", "0")
		cref("seed/no-seqs/L"+l, deBruijn(), Frame{}, "compress", "-level", l)
	}

	mt := text(1234, mib)
	zcli("cli/T4-19-B512KiB/text1MiB", mt, "-T4", "-19", "-B512KiB")
	zcli("cli/rsyncable/text1MiB", mt, "--rsyncable", "-T2", "-3")
	for _, name := range []string{"text256KiB", "zstd-format.md"} {
		zcli("cli/long27-19/"+name, inputs[name], "--long=27", "-19")
	}
	if !testing.Short() {
		zcli("cli/T4-19/text4MiB", inputs["text4MiB"], "-T4", "-19")
		zcli("cli/rsyncable/text4MiB", inputs["text4MiB"], "--rsyncable", "-T2", "-3")
		zcli("cli/long27-19/text4MiB", inputs["text4MiB"], "--long=27", "-19")
	}

	// Run the jobs in parallel, through the cache.
	out := make([][]byte, len(jobs))
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			k := sha256.New()
			k.Write([]byte(filepath.Base(j.tool) + "\x00" + strings.Join(j.args, "\x00") + "\x00"))
			k.Write(j.in)
			p := filepath.Join(dir, strings.ReplaceAll(j.name, "/", "__")+"-"+hex.EncodeToString(k.Sum(nil))[:12])
			if b, err := os.ReadFile(p); err == nil {
				out[i] = b
				return
			}
			b, err := runCmd(j.tool, j.in, j.args...)
			if err == nil {
				err = writeAtomic(dir, p, b)
			}
			out[i], errs[i] = b, err
		}()
	}
	wg.Wait()
	m := map[string]Frame{}
	for i, j := range jobs {
		if errs[i] != nil {
			t.Fatalf("decode corpus %s: %v", j.name, errs[i])
		}
		j.f.Data = out[i]
		m[j.name] = j.f
	}

	cat := func(name string, parts ...string) {
		var f Frame
		for _, p := range parts {
			g, ok := m[p]
			if !ok {
				t.Fatalf("decode corpus %s: no part %s", name, p)
			}
			f.Data = append(f.Data, g.Data...)
			f.Want = append(f.Want, g.Want...)
		}
		f.Want = append([]byte{}, f.Want...) // non-nil
		m[name] = f
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
	for k := range skip {
		delete(m, "skippable-frame/"+k)
	}
	return m
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

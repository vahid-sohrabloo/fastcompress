package creftest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// LZ4Bin returns the path of the lz4ref binary (lz4 v1.11.1 driver).
func LZ4Bin(t testing.TB) string {
	t.Helper()
	return tool(t, "ref/lz4ref/lz4ref", "run ref/fetch.sh --no-corpus && make -C ref/lz4ref")
}

// LZ4CLI returns the path of the lz4 v1.11.1 command line program built by
// ref/fetch.sh.
func LZ4CLI(t testing.TB) string {
	t.Helper()
	return tool(t, "ref/lz4/programs/lz4", "run ref/fetch.sh")
}

// LZ4Run feeds in to `lz4ref args...` and returns its stdout. It fails the
// test when lz4ref exits non-zero.
func LZ4Run(t testing.TB, in []byte, args ...string) []byte {
	t.Helper()
	return run(t, LZ4Bin(t), in, args...)
}

// LZ4Dec runs an lz4ref decoder mode (dec, decpartial, decdict, framedec).
// It returns the decoded bytes, or the decoder's error text ("ret -1", or
// an LZ4F error name) when lz4ref rejects the input (exit status 2).
func LZ4Dec(t testing.TB, in []byte, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(LZ4Bin(t), args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
		return out.Bytes(), fmt.Errorf("%s", bytes.TrimSpace(stderr.Bytes()))
	}
	if err != nil {
		t.Fatalf("lz4ref %v: %v: %s", args, err, stderr.String())
	}
	return out.Bytes(), nil
}

// LZ4TempFile writes b to a new file in the test's temporary directory and
// returns its path (for lz4ref's dict= arguments).
func LZ4TempFile(t testing.TB, b []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "lz4ref")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(f.Name())
}

// LZ4SplitBlocks splits the output of lz4ref's multi-block modes (a 4-byte
// little-endian length before each block) into blocks.
func LZ4SplitBlocks(t testing.TB, b []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for len(b) > 0 {
		if len(b) < 4 {
			t.Fatalf("lz4ref: truncated block list")
		}
		n := int(binary.LittleEndian.Uint32(b))
		b = b[4:]
		if n > len(b) {
			t.Fatalf("lz4ref: truncated block")
		}
		out = append(out, b[:n:n])
		b = b[n:]
	}
	return out
}

// LZ4Inputs returns the shared differential test inputs (see Inputs) plus a
// few cases that matter to LZ4 in particular: the boundaries of its minimum
// sizes (MFLIMIT 12, the 64 KB byU16 limit) and long runs.
func LZ4Inputs(t testing.TB) map[string][]byte {
	t.Helper()
	m := Inputs(t)
	for _, n := range []int{0, 1, 4, 11, 12, 13, 17, 64*kib + 10, 64*kib + 11, 64*kib + 12} {
		m[fmt.Sprintf("text-%d", n)] = text(uint64(n)+7, n)
	}
	m["zeros-1M"] = make([]byte, mib)
	m["random-70K"] = random(5, 70*kib)
	m["period3-200K"] = period(3, 200*kib)
	m["period7-100K"] = period(7, 100*kib)
	return m
}

//go:build long

package zstd

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// repeatReader yields n bytes of data repeated.
type repeatReader struct {
	data []byte
	pos  int
	n    int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), r.n)]
	k := 0
	for k < len(p) {
		c := copy(p[k:], r.data[r.pos:])
		k += c
		r.pos = (r.pos + c) % len(r.data)
	}
	r.n -= int64(k)
	return k, nil
}

// TestCrefStreamOverflow (Review Focus 3) streams 4.5 GiB, silesia.tar
// repeated, through one Writer at levels 1 and 3, and compares the
// SHA-256 of the output with `cref stream-pipe` fed the same bytes. The
// indices pass ZSTD_CURRENT_MAX (3500 MiB), so overflow correction runs
// while the window has an extDict segment (C's input buffer wraps every
// window + block bytes). Neither side holds the input or output in memory.
func TestCrefStreamOverflow(t *testing.T) {
	const total = 4608 << 20 // 4.5 GiB
	const chunk = 1 << 20
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "bench", "corpus", "silesia.tar"))
	if err != nil {
		t.Skip("bench/corpus/silesia.tar not found (run ref/fetch.sh)")
	}
	for _, level := range []int{1, 3} {
		start := time.Now()
		// C, in a separate process.
		cmd := exec.Command(creftest.Bin(t), "stream-pipe", "-level", strconv.Itoa(level), "-checksum", "1",
			"-chunk-size", strconv.Itoa(chunk))
		cmd.Stdin = &repeatReader{data: data, n: total}
		hc := sha256.New()
		cmd.Stdout = hc
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		// Go, meanwhile.
		hg := sha256.New()
		e, err := NewWriter(hg, WithEncoderLevel(EncoderLevelFromZstd(level)), WithEncoderCRC(true))
		if err != nil {
			t.Fatal(err)
		}
		var nOut int64
		cw := &countPtrWriter{w: hg, n: &nOut}
		e.Reset(cw)
		r := &repeatReader{data: data, n: total}
		buf := make([]byte, chunk)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				if _, err := e.Write(buf[:n]); err != nil {
					t.Fatal(err)
				}
			}
			if rerr == io.EOF {
				break
			}
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		if e.s.c.blockState.matchState.window.nbOverflowCorrections == 0 {
			t.Errorf("L%d: no overflow correction happened", level)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("cref: %v: %s", err, stderr.String())
		}
		if got, want := hg.Sum(nil), hc.Sum(nil); !bytes.Equal(got, want) {
			t.Fatalf("L%d: SHA-256 of the 4.5 GiB stream differs: Go %x, C %x", level, got, want)
		}
		t.Logf("L%d: %d bytes -> %d bytes, %d overflow corrections, identical to C (%v)", level, int64(total), nOut,
			e.s.c.blockState.matchState.window.nbOverflowCorrections, time.Since(start).Round(time.Second))
	}
}

type countPtrWriter struct {
	w io.Writer
	n *int64
}

func (c *countPtrWriter) Write(p []byte) (int, error) {
	*c.n += int64(len(p))
	return c.w.Write(p)
}

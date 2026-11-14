//go:build long

package zstd

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// TestLDMFinalizeDictCref requires finalizeDictionary at level 22 with a
// 64 MiB history to equal `cref finalize-dict`: the CDict then has window
// log 27, so C's analyzeEntropy compresses the samples (block mode,
// ZSTD_compressBegin_usingCDict) with long-distance matching. About 1.5 GiB
// for each of Go and C.
func TestLDMFinalizeDictCref(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	hist := make([]byte, 64<<20)
	for i := range hist {
		hist[i] = byte(r.Uint32())
	}
	var samples [][]byte
	for range 24 {
		part := make([]byte, 12<<10)
		for i := range part {
			part[i] = byte(r.Uint32())
		}
		var s []byte
		for k := range 5 {
			at := len(s)
			s = append(s, part...)
			s[at+r.IntN(len(part))] += byte(k)
		}
		samples = append(samples, s)
	}
	seqs := countLDM(t)
	got, err := finalizeDictionary(hist, samples, 22, 1, repStartValue, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *seqs == 0 {
		t.Fatal("the LDM produced no sequences")
	}
	sameDict(t, got, crefFinalize(t, hist, samples, 22, 1))
}

// TestLDMStreamOverflow streams 3.6 GiB (an 8 MiB input with long-distance
// repeats, repeated) through a level 1 context with LDM enabled explicitly
// and window log 20, and compares the SHA-256 of the output with `cref
// stream-pipe -ldm 1` fed the same bytes: the LDM window's indices pass
// ZSTD_CURRENT_MAX, so ZSTD_ldm_generateSequences corrects them (and
// ZSTD_ldm_reduceTable its hash table) while the window has an extDict.
func TestLDMStreamOverflow(t *testing.T) {
	const total = 3700 << 20
	const chunk = 1 << 20
	data := ldmInput(8 << 20)
	start := time.Now()
	cmd := exec.Command(creftest.Bin(t), "stream-pipe", "-level", "1", "-checksum", "1", "-window-log", "20",
		"-ldm", "1", "-chunk-size", strconv.Itoa(chunk))
	cmd.Stdin = &repeatReader{data: data, n: total}
	hc := sha256.New()
	cmd.Stdout = hc
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	hg := sha256.New()
	c := newCCtx()
	c.setLevel(1)
	c.setChecksum(true)
	if err := c.setWindowLog(20); err != nil {
		t.Fatal(err)
	}
	c.requestedParams.ldmParams.enableLdm = psEnable
	out := make([]byte, 0, cStreamOutSize)
	feed := func(in []byte, op endDirective) {
		for {
			o, consumed, remaining, err := c.compressStream2(out, in, op)
			if err != nil {
				t.Fatal(err)
			}
			hg.Write(o)
			in = in[consumed:]
			if op == eContinue && len(in) == 0 || op != eContinue && remaining == 0 {
				return
			}
		}
	}
	rd := &repeatReader{data: data, n: total}
	buf := make([]byte, chunk)
	for {
		n, err := io.ReadFull(rd, buf)
		if n > 0 {
			feed(buf[:n], eContinue)
		}
		if err != nil {
			break
		}
	}
	feed(nil, eEnd)
	corrections := c.ldmState.window.nbOverflowCorrections
	if corrections == 0 {
		t.Error("no LDM overflow correction happened")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cref: %v: %s", err, stderr.String())
	}
	if got, want := hg.Sum(nil), hc.Sum(nil); !bytes.Equal(got, want) {
		t.Fatalf("SHA-256 of the stream differs: Go %x, C %x", got, want)
	}
	t.Logf("%d bytes, %d LDM overflow corrections, identical to C (%v)", int64(total), corrections, time.Since(start).Round(time.Second))
}

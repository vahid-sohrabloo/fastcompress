//go:build long

package zstd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/vahid-sohrabloo/fastzstd/internal/creftest"
)

// TestDecompressLongWindowCref decodes `zstd --long=27 -19` frames of
// silesia.tar (128 MiB window, cached: the first run takes minutes) block
// by block. Once the history exceeds 16 MiB, blocks with many long offsets
// take the prefetch decoder (decompressSequencesLongBody), which the
// counter shows; the output must equal the input, in the frame decoder's
// mode and in streaming mode.
func TestDecompressLongWindowCref(t *testing.T) {
	p := filepath.Join("..", "bench", "corpus", "silesia.tar")
	src, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("%s: %v", p, err)
	}
	frame := creftest.CachedCLI(t, "long/long27-19/silesia.tar", src, "--long=27", "-19")
	var zfh frameHeader
	if need, e := getFrameHeaderAdvanced(&zfh, frame); need != 0 || e != 0 || zfh.windowSize <= 16<<20 {
		t.Fatalf("frame header: need %d err %d window %d", need, e, zfh.windowSize)
	}
	for _, m := range []struct {
		name string
		mode blockMode
	}{{"frame", bmFrame}, {"stream", bmStream}} {
		long0 := seqLongBodyBlocks.Load()
		got, e := decodeFrameByBlocks(new(dctx), frame, len(src), 0, -1, m.mode, false)
		if e != 0 {
			t.Fatalf("%s: error %d", m.name, e)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("%s: output differs", m.name)
		}
		nLong := seqLongBodyBlocks.Load() - long0
		if nLong == 0 {
			t.Fatalf("%s: no block took the prefetch decoder", m.name)
		}
		t.Logf("%s: window %d, %d of %d MiB, %d blocks took the prefetch decoder", m.name, zfh.windowSize, len(frame)>>20, len(src)>>20, nLong)
	}
}

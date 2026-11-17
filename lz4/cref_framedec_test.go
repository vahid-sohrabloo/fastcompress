package lz4

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// frameErrName maps a frame decoding error to C's LZ4F error name ("incomplete"
// for truncated input, lz4ref's name for an unfinished frame).
func frameErrName(err error) string {
	switch err {
	case nil:
		return ""
	case io.ErrUnexpectedEOF:
		return "incomplete"
	case ErrFrameType:
		return "ERROR_frameType_unknown"
	case ErrReservedFlag:
		return "ERROR_reservedFlag_set"
	case ErrHeaderVersion:
		return "ERROR_headerVersion_wrong"
	case ErrMaxBlockSize:
		return "ERROR_maxBlockSize_invalid"
	case ErrHeaderChecksum:
		return "ERROR_headerChecksum_invalid"
	case ErrBlockChecksum:
		return "ERROR_blockChecksum_invalid"
	case ErrContentChecksum:
		return "ERROR_contentChecksum_invalid"
	case ErrFrameSize:
		return "ERROR_frameSize_wrong"
	case ErrDecompressionFailed:
		return "ERROR_decompressionFailed"
	}
	return err.Error()
}

func TestFrameDecodeCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	dict := m["text-65547"][:20000]
	df := creftest.LZ4TempFile(t, dict)
	type fr struct {
		name  string
		frame []byte
		dict  []byte
	}
	var frames []fr
	for _, name := range []string{"text128KiB", "text-17", "random-70K", "period3-200K"} {
		in := m[name]
		for ci, c := range framePrefsCases() {
			if ci%3 != 0 {
				continue
			}
			p := c.prefs(len(in))
			var buf bytes.Buffer
			w := NewWriter(&buf, WithPreferences(*p))
			w.Write(in[:len(in)/2])
			w.WriteUncompressed(in[len(in)/2 : len(in)*3/4])
			w.Write(in[len(in)*3/4:])
			w.Close()
			frames = append(frames, fr{fmt.Sprintf("%s/%d", name, ci), buf.Bytes(), nil})
			var dbuf bytes.Buffer
			w = NewWriter(&dbuf, WithPreferences(*p), WithDict(dict))
			w.Write(in)
			w.Close()
			frames = append(frames, fr{fmt.Sprintf("%s/%d/dict", name, ci), dbuf.Bytes(), dict})
		}
	}
	// two frames and a skippable frame, concatenated
	cat := append(append([]byte{}, frames[0].frame...), 0x5A, 0x2A, 0x4D, 0x18, 3, 0, 0, 0, 1, 2, 3)
	cat = append(cat, frames[2].frame...)
	frames = append(frames, fr{"concat", cat, nil})
	for _, f := range frames {
		cases := append([][]byte{f.frame}, mutations(f.frame, uint64(len(f.frame)), 25)...)
		for i, b := range cases {
			t.Run(fmt.Sprintf("%s/m%d", f.name, i), func(t *testing.T) {
				args := []string{"framedec"}
				if f.dict != nil {
					args = append(args, "dict="+df)
				}
				want, cerr := creftest.LZ4Dec(t, b, args...)
				got, gerr := AppendDecompressFrame(nil, b, f.dict)
				cname := ""
				if cerr != nil {
					cname = cerr.Error()
				}
				if frameErrName(gerr) != cname {
					t.Fatalf("error %q, C %q", frameErrName(gerr), cname)
				}
				if cerr == nil && !bytes.Equal(got, want) {
					t.Fatalf("output differs: %d bytes, C %d", len(got), len(want))
				}
				rgot, rerr := io.ReadAll(NewReaderDict(bytes.NewReader(b), f.dict))
				if frameErrName(rerr) != cname {
					t.Fatalf("Reader error %q, C %q", frameErrName(rerr), cname)
				}
				if cerr == nil && !bytes.Equal(rgot, want) {
					t.Fatalf("Reader output differs")
				}
			})
		}
	}
}

func TestCompressFrameCDictCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	dict := m["text-65547"]
	cd := NewCDict(dict)
	df := creftest.LZ4TempFile(t, dict)
	for _, name := range frameInputs(t) {
		in := m[name]
		for ci, c := range framePrefsCases() {
			t.Run(fmt.Sprintf("%s/%d", name, ci), func(t *testing.T) {
				want := creftest.LZ4Run(t, in, append([]string{"frame", "dict=" + df, "cdict=1"}, c.args()...)...)
				p := c.prefs(len(in))
				dst := make([]byte, CompressFrameBound(len(in), p))
				n, err := CompressFrameUsingCDict(dst, in, cd, p)
				if err != nil || !bytes.Equal(dst[:n], want) {
					t.Fatalf("frame differs from C: %v, %d bytes, C %d", err, n, len(want))
				}
				out, err := creftest.LZ4Dec(t, dst[:n], "framedec", "dict="+df)
				if err != nil || !bytes.Equal(out, in) {
					t.Fatalf("C decoding: %v", err)
				}
			})
		}
	}
	_ = strconv.Itoa
}

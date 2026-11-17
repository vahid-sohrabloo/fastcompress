package lz4

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// mtInputs are inputs over the 1 MB job size: text, runs and random data
// mixed, and (without -short) a Silesia file over two 4 MB jobs.
func mtInputs(t *testing.T) map[string][]byte {
	m := creftest.LZ4Inputs(t)
	var mix []byte
	for i := 0; len(mix) < 2600<<10; i++ {
		mix = append(mix, m["text128KiB"]...)
		mix = append(mix, m[[]string{"random-70K", "period3-200K", "text-65547"}[i%3]]...)
	}
	out := map[string][]byte{"mix": mix}
	if !testing.Short() {
		if b, ok := m["silesia/mozilla"]; ok {
			out["mozilla9M"] = b[:9<<20]
		}
	}
	return out
}

// mtPlans are Writer plans (as framePlans); "oneshot" is CompressFrame.
var mtPlans = []string{"oneshot", "w3000000", "w100000,w1500000,f,w70000", "w1048576,u5000,w1048576,f,f",
	"w65536,w65536,w65536,u1200000,w900000", "w7,w2000000,f,w1"}

// TestFrameMTCref compares frames cut into jobs (NbWorkers 1, 3 and 8)
// with C's (nbWorkers = 1: the output does not depend on the number),
// one-shot and through the Writer with writes, uncompressed writes and
// flushes, with and without dictionaries.
func TestFrameMTCref(t *testing.T) {
	dictSrc := creftest.LZ4Inputs(t)["text-65547"]
	type mtCase struct {
		level       int
		bsid        BlockSizeID
		indep       bool
		cchk, bchk  bool
		af, favor   bool
		dict, cdict bool
	}
	var cases []mtCase
	for _, level := range []int{1, -3, 2, 3, 9, 10} {
		for _, bsid := range []BlockSizeID{4, 5, 6, 7} {
			for _, indep := range []bool{false, true} {
				c := mtCase{level: level, bsid: bsid, indep: indep, cchk: bsid == 4, bchk: bsid == 5,
					af: bsid == 6, favor: level == 10 && !indep, dict: bsid == 5, cdict: level == 1}
				cases = append(cases, c)
			}
		}
	}
	cases = append(cases, mtCase{level: 12, bsid: 4}, mtCase{level: 12, bsid: 6, indep: true, dict: true, cdict: true})
	for name, in := range mtInputs(t) {
		for pi, plan := range mtPlans {
			planStr, ops := planFor(plan, len(in))
			for ci, c := range cases {
				if testing.Short() && (ci+pi)%7 != 0 {
					continue
				}
				if c.bsid == 7 && len(in) < 5<<20 && pi > 1 {
					continue
				}
				t.Run(fmt.Sprintf("%s/p%d/%d", name, pi, ci), func(t *testing.T) {
					p := &Preferences{CompressionLevel: c.level, AutoFlush: c.af, FavorDecSpeed: c.favor}
					p.BlockSizeID = c.bsid
					p.BlockIndependent = c.indep
					p.ContentChecksum = c.cchk
					p.BlockChecksum = c.bchk
					args := []string{"frame", "workers=1", "level=" + strconv.Itoa(c.level), "bsid=" + strconv.Itoa(int(c.bsid)),
						"linked=" + strconv.Itoa(1-b2i(c.indep)), "cchk=" + strconv.Itoa(b2i(c.cchk)),
						"bchk=" + strconv.Itoa(b2i(c.bchk)), "favor=" + strconv.Itoa(b2i(c.favor)),
						"autoflush=" + strconv.Itoa(b2i(c.af))}
					var dict []byte
					if c.dict {
						dict = dictSrc
						args = append(args, "dict="+creftest.LZ4TempFile(t, dict), "cdict="+strconv.Itoa(b2i(c.cdict)))
					}
					if plan != "oneshot" {
						args = append(args, "plan="+planStr)
					} else if c.dict && !c.cdict {
						return // lz4ref one-shot with a raw dictionary is begin+update+end
					}
					want := creftest.LZ4Run(t, in, args...)
					for _, nw := range []int{1, 3, 8} {
						p.NbWorkers = nw
						var got []byte
						if plan == "oneshot" {
							got = make([]byte, CompressFrameBound(len(in), p))
							var n int
							var err error
							if c.dict {
								n, err = CompressFrameUsingCDict(got, in, NewCDict(dict), p)
							} else {
								n, err = CompressFrame(got, in, p)
							}
							if err != nil {
								t.Fatal(err)
							}
							got = got[:n]
						} else {
							var buf bytes.Buffer
							opts := []WriterOption{WithPreferences(*p)}
							if c.dict && c.cdict {
								opts = append(opts, WithCDict(NewCDict(dict)))
							} else if c.dict {
								opts = append(opts, WithDict(dict))
							}
							w := NewWriter(&buf, opts...)
							pos := 0
							for _, op := range ops {
								var err error
								switch op[0] {
								case 'f':
									err = w.Flush()
								case 'w':
									_, err = w.Write(append([]byte{}, in[pos:pos+op[1]]...))
								case 'u':
									_, err = w.WriteUncompressed(append([]byte{}, in[pos:pos+op[1]]...))
								}
								if err != nil {
									t.Fatal(err)
								}
								pos += op[1]
							}
							if err := w.Close(); err != nil {
								t.Fatal(err)
							}
							got = buf.Bytes()
						}
						if !bytes.Equal(got, want) {
							t.Fatalf("NbWorkers %d: frame differs from C's: %d bytes, C %d", nw, len(got), len(want))
						}
					}
					dec, err := AppendDecompressFrame(nil, want, dict)
					if err != nil || !bytes.Equal(dec, in) {
						t.Fatalf("decoding C's frame: %v", err)
					}
				})
			}
		}
	}
}

// TestFrameCompressorMT runs Begin, Update, Flush and End with NbWorkers
// against the Writer's frame, with dst at the bound and with
// CompressFrameBound for the one-shot method; the frames must be equal and
// decode.
func TestFrameCompressorMT(t *testing.T) {
	in := mtInputs(t)["mix"]
	for _, indep := range []bool{false, true} {
		for _, level := range []int{1, 9} {
			p := &Preferences{CompressionLevel: level, NbWorkers: 4}
			p.BlockIndependent = indep
			var buf bytes.Buffer
			w := NewWriter(&buf, WithPreferences(*p))
			var fc FrameCompressor
			out := make([]byte, maxFHSize)
			n, err := fc.Begin(out, p)
			if err != nil {
				t.Fatal(err)
			}
			frame := append([]byte(nil), out[:n]...)
			for off, i := 0, 0; off < len(in); i++ {
				step := []int{1000, 300000, 1 << 20, 7}[i%4]
				piece := in[off:min(off+step, len(in))]
				if _, err := w.Write(piece); err != nil {
					t.Fatal(err)
				}
				dst := make([]byte, CompressBoundFrame(len(piece), p))
				n, err := fc.Update(dst, piece)
				if err != nil {
					t.Fatal(err)
				}
				frame = append(frame, dst[:n]...)
				if i%5 == 4 {
					if err := w.Flush(); err != nil {
						t.Fatal(err)
					}
					dst := make([]byte, CompressBoundFrame(0, p))
					n, err := fc.Flush(dst)
					if err != nil {
						t.Fatal(err)
					}
					frame = append(frame, dst[:n]...)
				}
				off += len(piece)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			dst := make([]byte, CompressBoundFrame(0, p))
			n, err = fc.End(dst)
			if err != nil {
				t.Fatal(err)
			}
			frame = append(frame, dst[:n]...)
			if !bytes.Equal(frame, buf.Bytes()) {
				t.Fatalf("indep %v level %d: FrameCompressor %d bytes, Writer %d", indep, level, len(frame), buf.Len())
			}
			dec, err := io.ReadAll(NewReader(bytes.NewReader(frame)))
			if err != nil || !bytes.Equal(dec, in) {
				t.Fatalf("decoding: %v", err)
			}
			one := make([]byte, CompressFrameBound(len(in), p))
			n, err = fc.CompressFrame(one, in, p)
			if err != nil {
				t.Fatal(err)
			}
			p0 := *p
			p0.NbWorkers = 1
			two, err := AppendFrame(nil, in, &p0)
			if err != nil || !bytes.Equal(one[:n], two) {
				t.Fatalf("one-shot with 4 and 1 workers differ: %v", err)
			}
		}
	}
}

// TestFrameMTStable compresses with UpdateStable, from chunks of one buffer
// (jobs read their history in place across calls) and from separate
// copies, and with Update: the frames must equal C's (workers=1, plan of
// the same writes).
func TestFrameMTStable(t *testing.T) {
	in := mtInputs(t)["mix"]
	for _, level := range []int{1, 2, 9} {
		for _, chunk := range []int{1 << 20, 3 << 20 / 2, 70000} {
			p := &Preferences{CompressionLevel: level, NbWorkers: 3}
			var plan []string
			for off := 0; off < len(in); off += chunk {
				plan = append(plan, "w"+strconv.Itoa(min(chunk, len(in)-off)))
			}
			want := creftest.LZ4Run(t, in, "frame", "workers=1", "level="+strconv.Itoa(level), "linked=1",
				"plan="+strings.Join(plan, ","))
			for mode := 0; mode < 3; mode++ {
				var fc FrameCompressor
				frame := make([]byte, maxFHSize)
				n, err := fc.Begin(frame, p)
				if err != nil {
					t.Fatal(err)
				}
				frame = frame[:n]
				for off := 0; off < len(in); off += chunk {
					piece := in[off:min(off+chunk, len(in))]
					dst := make([]byte, CompressBoundFrame(len(piece), p))
					switch mode {
					case 0:
						n, err = fc.UpdateStable(dst, piece)
					case 1:
						n, err = fc.UpdateStable(dst, append([]byte(nil), piece...))
					default:
						n, err = fc.Update(dst, piece)
					}
					if err != nil {
						t.Fatal(err)
					}
					frame = append(frame, dst[:n]...)
				}
				dst := make([]byte, CompressBoundFrame(0, p))
				n, err = fc.End(dst)
				if err != nil {
					t.Fatal(err)
				}
				frame = append(frame, dst[:n]...)
				if !bytes.Equal(frame, want) {
					t.Fatalf("level %d chunk %d mode %d: %d bytes, C %d", level, chunk, mode, len(frame), len(want))
				}
			}
		}
	}
}

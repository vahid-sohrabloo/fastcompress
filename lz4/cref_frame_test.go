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

type framePrefsCase struct {
	level                  int
	bsid                   BlockSizeID
	indep, cchk, bchk, csz bool
	dictID                 uint32
	favor, autoflush       bool
}

func (c framePrefsCase) args() []string {
	a := []string{"level=" + strconv.Itoa(c.level), "bsid=" + strconv.Itoa(int(c.bsid)),
		"linked=" + strconv.Itoa(1-b2i(c.indep)), "cchk=" + strconv.Itoa(b2i(c.cchk)),
		"bchk=" + strconv.Itoa(b2i(c.bchk)), "csize=" + strconv.Itoa(b2i(c.csz)),
		"dictid=" + strconv.Itoa(int(c.dictID)), "favor=" + strconv.Itoa(b2i(c.favor)),
		"autoflush=" + strconv.Itoa(b2i(c.autoflush))}
	return a
}

func (c framePrefsCase) prefs(n int) *Preferences {
	p := &Preferences{CompressionLevel: c.level, AutoFlush: c.autoflush, FavorDecSpeed: c.favor}
	p.BlockSizeID = c.bsid
	p.BlockIndependent = c.indep
	p.ContentChecksum = c.cchk
	p.BlockChecksum = c.bchk
	p.DictID = c.dictID
	if c.csz {
		p.ContentSize = uint64(n)
	}
	return p
}

func framePrefsCases() []framePrefsCase {
	var out []framePrefsCase
	for _, level := range []int{-5, 0, 1, 2, 3, 9, 10, 12} {
		for _, bsid := range []BlockSizeID{0, 4, 5, 7} {
			for _, indep := range []bool{false, true} {
				out = append(out, framePrefsCase{level: level, bsid: bsid, indep: indep,
					cchk: bsid == 4, bchk: bsid == 5, csz: indep, dictID: uint32(bsid) * 7, favor: level >= 10 && indep})
			}
		}
	}
	if testing.Short() {
		out = out[:len(out):len(out)]
		var s []framePrefsCase
		for i, c := range out {
			if i%5 == 0 {
				s = append(s, c)
			}
		}
		out = s
	}
	return out
}

func frameInputs(t *testing.T) []string {
	if testing.Short() {
		return []string{"size0", "text-17", "text128KiB", "random-70K"}
	}
	return []string{"size0", "size1", "text-17", "text128KiB", "random-70K", "period3-200K", "zeros-1M", "text-65547"}
}

func TestCompressFrameCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	for _, name := range frameInputs(t) {
		in := m[name]
		for ci, c := range framePrefsCases() {
			t.Run(fmt.Sprintf("%s/%d", name, ci), func(t *testing.T) {
				want := creftest.LZ4Run(t, in, append([]string{"frame"}, c.args()...)...)
				dst := make([]byte, CompressFrameBound(len(in), c.prefs(len(in))))
				n, err := CompressFrame(dst, in, c.prefs(len(in)))
				if err != nil || !bytes.Equal(dst[:n], want) {
					t.Fatalf("frame differs from C: %v, %d bytes, C %d", err, n, len(want))
				}
				got, err := AppendDecompressFrame(nil, want, nil)
				if err != nil || !bytes.Equal(got, in) {
					t.Fatalf("decoding C's frame: %v", err)
				}
			})
		}
	}
}

// plans are lz4ref frame plans; each wN takes min(N, rest) bytes and the
// rest of the input is written last.
var framePlans = []string{
	"w100", "w1000,w1000,w1000", "w70000,f,w5", "w5,u300,w200000", "u70000,w70000,f,u10",
	"w65536,w65536,f,f", "w131072,w3,w262144", "w16,w16,w16,f,w64,w300000",
}

func planFor(plan string, n int) (string, [][2]int) {
	var steps []string
	var ops [][2]int // kind ('w','u','f'), size
	rest := n
	for _, s := range strings.Split(plan, ",") {
		if s == "f" {
			steps = append(steps, "f")
			ops = append(ops, [2]int{'f', 0})
			continue
		}
		sz, _ := strconv.Atoi(s[1:])
		sz = min(sz, rest)
		rest -= sz
		steps = append(steps, s[:1]+strconv.Itoa(sz))
		ops = append(ops, [2]int{int(s[0]), sz})
	}
	if rest > 0 {
		steps = append(steps, "w"+strconv.Itoa(rest))
		ops = append(ops, [2]int{'w', rest})
	}
	return strings.Join(steps, ","), ops
}

func TestWriterCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	dictSrc := m["text-65547"]
	dicts := map[string][]byte{"none": nil, "big": dictSrc, "small": dictSrc[:5000]}
	for _, name := range frameInputs(t) {
		in := m[name]
		for pi, plan := range framePlans {
			planStr, ops := planFor(plan, len(in))
			for ci, c := range framePrefsCases() {
				for _, af := range []bool{false, true} {
					c.autoflush = af
					c.csz = false
					for dn, dict := range dicts {
						for _, useCDict := range []bool{false, true} {
							if dict == nil && useCDict {
								continue
							}
							t.Run(fmt.Sprintf("%s/p%d/%d/af%v/%s/cd%v", name, pi, ci, af, dn, useCDict), func(t *testing.T) {
								args := append([]string{"frame", "plan=" + planStr}, c.args()...)
								var opts []WriterOption
								opts = append(opts, WithPreferences(*c.prefs(len(in))))
								if dict != nil {
									args = append(args, "dict="+creftest.LZ4TempFile(t, dict), "cdict="+strconv.Itoa(b2i(useCDict)))
									if useCDict {
										opts = append(opts, WithCDict(NewCDict(dict)))
									} else {
										opts = append(opts, WithDict(dict))
									}
								}
								want := creftest.LZ4Run(t, in, args...)
								var buf bytes.Buffer
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
								if !bytes.Equal(buf.Bytes(), want) {
									t.Fatalf("frame differs from C: %d bytes, C %d", buf.Len(), len(want))
								}
								got, err := AppendDecompressFrame(nil, want, dict)
								if err != nil || !bytes.Equal(got, in) {
									t.Fatalf("decoding C's frame: %v", err)
								}
								r := NewReaderDict(bytes.NewReader(want), dict)
								got, err = io.ReadAll(r)
								if err != nil || !bytes.Equal(got, in) {
									t.Fatalf("Reader on C's frame: %v", err)
								}
							})
						}
					}
				}
			}
			if testing.Short() && pi > 2 {
				break
			}
		}
	}
}

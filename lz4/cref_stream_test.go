package lz4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// pieces cuts in into the given sizes; the last one takes the rest.
func pieces(in []byte, sizes []int) []int {
	var out []int
	rest := len(in)
	for _, s := range sizes {
		if s > rest {
			s = rest
		}
		out = append(out, s)
		rest -= s
	}
	if rest > 0 || len(out) == 0 {
		out = append(out, rest)
	}
	return out
}

func joinSizes(s []int) string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

type streamCase struct {
	layout, dictmode, capMode string
	dict                      []byte
}

// goStream runs the lz4ref "stream algo=fast" scenario in Go.
func goStreamFast(in []byte, sizes []int, level int, sc streamCase) [][]byte {
	var s Stream
	var big []byte
	dictSize := len(sc.dict)
	if sc.layout == "prefix" {
		big = append(append([]byte{}, sc.dict...), in...)
	}
	if sc.dict != nil {
		switch sc.dictmode {
		case "attach":
			d := new(Stream)
			d.LoadDictSlow(sc.dict)
			s.ResetFast()
			s.AttachDictionary(d)
		case "slow":
			if big != nil {
				s.LoadDictSlow(big[:dictSize])
			} else {
				s.LoadDictSlow(sc.dict)
			}
		default:
			if big != nil {
				s.LoadDict(big[:dictSize])
			} else {
				s.LoadDict(sc.dict)
			}
		}
	}
	saveBuf := make([]byte, 64*kb)
	var out [][]byte
	pos := 0
	for _, n := range sizes {
		var src []byte
		if big != nil {
			src = big[dictSize+pos : dictSize+pos+n]
		} else {
			src = append(make([]byte, 0, n+1), in[pos:pos+n]...)
		}
		capa := CompressBound(n)
		if sc.capMode == "tight" {
			capa = max(n-1, 1)
		}
		dst := make([]byte, capa)
		r := s.CompressFastContinue(dst, src, level)
		out = append(out, dst[:r])
		if sc.layout == "save" {
			s.SaveDict(saveBuf)
		}
		pos += n
	}
	return out
}

func streamInputs(t *testing.T) map[string][]byte {
	m := creftest.LZ4Inputs(t)
	keep := map[string][]byte{}
	for name, b := range m {
		if len(b) <= 2<<20 || !testing.Short() {
			keep[name] = b
		}
	}
	return keep
}

var streamSizeSets = [][]int{
	{1 << 30},
	{1000, 1000, 1000},
	{3, 5, 100, 4000, 70000},
	{65536, 65536, 65536},
	{200000},
	{16, 16, 2048, 2047, 300000},
}

func TestStreamFastCref(t *testing.T) {
	m := streamInputs(t)
	dict := creftest.LZ4Inputs(t)["text-65547"]
	small := dict[:3000]
	cases := []streamCase{
		{layout: "prefix"}, {layout: "ext"}, {layout: "save"},
		{layout: "prefix", dict: dict}, {layout: "ext", dict: dict}, {layout: "save", dict: small},
		{layout: "ext", dict: dict, dictmode: "slow"}, {layout: "prefix", dict: small, dictmode: "slow"},
		{layout: "ext", dict: dict, dictmode: "attach"}, {layout: "ext", dict: small, dictmode: "attach"},
		{layout: "ext", capMode: "tight"}, {layout: "prefix", dict: small, capMode: "tight"},
		{layout: "ext", dict: []byte("tiny")},
	}
	for name, in := range m {
		for si, ss := range streamSizeSets {
			sizes := pieces(in, ss)
			for ci, sc := range cases {
				for _, level := range []int{1, 5} {
					t.Run(fmt.Sprintf("%s/s%d/c%d/a%d", name, si, ci, level), func(t *testing.T) {
						args := []string{"stream", "algo=fast", "level=" + strconv.Itoa(level), "sizes=" + joinSizes(sizes),
							"layout=" + sc.layout}
						if sc.dict != nil {
							args = append(args, "dict="+creftest.LZ4TempFile(t, sc.dict))
						}
						if sc.dictmode != "" {
							args = append(args, "dictmode="+sc.dictmode)
						}
						if sc.capMode != "" {
							args = append(args, "cap="+sc.capMode)
						}
						want := creftest.LZ4SplitBlocks(t, creftest.LZ4Run(t, in, args...))
						got := goStreamFast(in, sizes, level, sc)
						compareBlocks(t, got, want)
					})
				}
			}
		}
		if testing.Short() {
			break
		}
	}
}

func compareBlocks(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d blocks, C %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("block %d differs from C: %d bytes, C %d", i, len(got[i]), len(want[i]))
		}
	}
}

func TestFastResetCref(t *testing.T) {
	m := streamInputs(t)
	for name, in := range m {
		for si, ss := range streamSizeSets {
			sizes := pieces(in, ss)
			for _, a := range []int{1, 4} {
				t.Run(fmt.Sprintf("%s/s%d/a%d", name, si, a), func(t *testing.T) {
					want := creftest.LZ4SplitBlocks(t, creftest.LZ4Run(t, in, "fastreset", "accel="+strconv.Itoa(a), "sizes="+joinSizes(sizes)))
					var s Stream
					var got [][]byte
					pos := 0
					for _, n := range sizes {
						src := append(make([]byte, 0, n+1), in[pos:pos+n]...)
						dst := make([]byte, CompressBound(n))
						r := s.CompressFast(dst, src, a)
						got = append(got, dst[:r])
						pos += n
					}
					compareBlocks(t, got, want)
				})
			}
		}
	}
}

func TestDestSizeCref(t *testing.T) {
	m := streamInputs(t)
	for name, in := range m {
		for _, target := range []int{1, 2, 10, 17, 100, 1000, 5000, 65536, 300000} {
			for _, a := range []int{1, 3} {
				t.Run(fmt.Sprintf("%s/t%d/a%d", name, target, a), func(t *testing.T) {
					out := creftest.LZ4Run(t, in, "destsize", "accel="+strconv.Itoa(a), "target="+strconv.Itoa(target))
					wantConsumed := int(binary.LittleEndian.Uint32(out))
					want := out[4:]
					dst := make([]byte, target)
					var s Stream
					n, consumed := s.CompressDestSize(dst, in, a)
					if consumed != wantConsumed || !bytes.Equal(dst[:n], want) {
						t.Fatalf("got %d bytes from %d, C %d from %d", n, consumed, len(want), wantConsumed)
					}
				})
			}
		}
	}
}

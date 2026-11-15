package lz4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

func hcLevels() []int {
	if testing.Short() {
		return []int{2, 3, 9, 10, 12}
	}
	return []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
}

func TestCompressHCCref(t *testing.T) {
	names, m := sortedInputs(t)
	for _, name := range names {
		in := m[name]
		if testing.Short() && len(in) > 300<<10 {
			continue
		}
		for _, level := range hcLevels() {
			for _, favor := range []bool{false, true} {
				if favor && level < 10 && level != 2 {
					continue
				}
				t.Run(fmt.Sprintf("%s/l%d/f%v", name, level, favor), func(t *testing.T) {
					args := []string{"hc", "level=" + strconv.Itoa(level)}
					var got []byte
					dst := make([]byte, CompressBound(len(in)))
					if favor {
						args = append(args, "favor=1")
						s := NewStreamHC(LevelHCDefault)
						s.FavorDecompressionSpeed(true)
						got = dst[:s.Compress(dst, in, level)]
					} else {
						got = dst[:CompressHC(dst, in, level)]
					}
					want := creftest.LZ4Run(t, in, args...)
					if !bytes.Equal(got, want) {
						t.Fatalf("output differs from C: %d bytes, C %d", len(got), len(want))
					}
				})
			}
		}
	}
}

func TestCompressHCLimitedCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	for _, name := range []string{"text128KiB", "random-70K", "period3-200K", "text-17"} {
		in := m[name]
		for _, level := range []int{2, 4, 9, 11} {
			for _, capa := range []int{len(in) / 2, len(in) / 3, len(in) - 1, 20} {
				t.Run(fmt.Sprintf("%s/l%d/c%d", name, level, capa), func(t *testing.T) {
					want := creftest.LZ4Run(t, in, "hc", "level="+strconv.Itoa(level), "cap="+strconv.Itoa(capa))
					dst := make([]byte, capa)
					got := dst[:CompressHC(dst, in, level)]
					if !bytes.Equal(got, want) {
						t.Fatalf("output differs from C: %d bytes, C %d", len(got), len(want))
					}
				})
			}
		}
	}
}

func TestHCResetCref(t *testing.T) {
	m := streamInputs(t)
	for name, in := range m {
		if testing.Short() && len(in) > 300<<10 {
			continue
		}
		big := len(in) > 1<<20
		for si, ss := range streamSizeSets {
			if big && si != 3 {
				continue
			}
			sizes := pieces(in, ss)
			for _, level := range []int{2, 5, 10} {
				if big && level == 10 {
					continue
				}
				t.Run(fmt.Sprintf("%s/s%d/l%d", name, si, level), func(t *testing.T) {
					want := creftest.LZ4SplitBlocks(t, creftest.LZ4Run(t, in, "hcreset", "level="+strconv.Itoa(level), "sizes="+joinSizes(sizes)))
					s := NewStreamHC(LevelHCDefault)
					var got [][]byte
					pos := 0
					for _, n := range sizes {
						src := append(make([]byte, 0, n+1), in[pos:pos+n]...)
						dst := make([]byte, CompressBound(n))
						got = append(got, dst[:s.Compress(dst, src, level)])
						pos += n
					}
					compareBlocks(t, got, want)
				})
			}
		}
	}
}

func TestHCDestSizeCref(t *testing.T) {
	m := streamInputs(t)
	for name, in := range m {
		if testing.Short() && len(in) > 300<<10 {
			continue
		}
		big := len(in) > 1<<20
		for _, target := range []int{1, 10, 17, 100, 1000, 5000, 65536, 300000} {
			for _, level := range []int{2, 3, 9, 10, 12} {
				if big && (level >= 10 || target < 65536) {
					continue
				}
				t.Run(fmt.Sprintf("%s/t%d/l%d", name, target, level), func(t *testing.T) {
					out := creftest.LZ4Run(t, in, "hcdestsize", "level="+strconv.Itoa(level), "target="+strconv.Itoa(target))
					wantConsumed := int(binary.LittleEndian.Uint32(out))
					dst := make([]byte, target)
					n, consumed := CompressHCDestSize(dst, in, level)
					if consumed != wantConsumed || !bytes.Equal(dst[:n], out[4:]) {
						t.Fatalf("got %d bytes from %d, C %d from %d", n, consumed, len(out)-4, wantConsumed)
					}
				})
			}
		}
	}
}

func goStreamHC(in []byte, sizes []int, level int, sc streamCase, favor bool) [][]byte {
	s := NewStreamHC(level)
	if favor {
		s.FavorDecompressionSpeed(true)
	}
	var big []byte
	dictSize := len(sc.dict)
	if sc.layout == "prefix" {
		big = append(append([]byte{}, sc.dict...), in...)
	}
	if sc.dict != nil {
		if sc.dictmode == "attach" {
			d := NewStreamHC(level)
			d.LoadDict(sc.dict)
			s.AttachDictionary(d)
		} else if big != nil {
			s.LoadDict(big[:dictSize])
		} else {
			s.LoadDict(sc.dict)
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
		out = append(out, dst[:s.CompressContinue(dst, src)])
		if sc.layout == "save" {
			s.SaveDict(saveBuf)
		}
		pos += n
	}
	return out
}

func TestStreamHCCref(t *testing.T) {
	m := streamInputs(t)
	dict := creftest.LZ4Inputs(t)["text-65547"]
	small := dict[:3000]
	cases := []streamCase{
		{layout: "prefix"}, {layout: "ext"}, {layout: "save"},
		{layout: "prefix", dict: dict}, {layout: "ext", dict: dict}, {layout: "save", dict: small},
		{layout: "ext", dict: dict, dictmode: "attach"}, {layout: "ext", dict: small, dictmode: "attach"},
		{layout: "ext", capMode: "tight"}, {layout: "prefix", dict: small, capMode: "tight"},
		{layout: "ext", dict: []byte("tiny")},
	}
	for name, in := range m {
		if testing.Short() && len(in) > 300<<10 {
			continue
		}
		// inputs over 1 MiB (Silesia): fewer combinations, the slow levels once
		big := len(in) > 1<<20
		for si, ss := range streamSizeSets {
			if big && si != 0 && si != 3 {
				continue
			}
			sizes := pieces(in, ss)
			for ci, sc := range cases {
				if big && ci > 4 {
					continue
				}
				for _, level := range []int{2, 4, 9, 10, 12} {
					if big && (level == 4 || (level >= 10 && (si != 3 || ci != 1))) {
						continue
					}
					for _, favor := range []bool{false, level >= 10} {
						if big && favor {
							continue
						}
						t.Run(fmt.Sprintf("%s/s%d/c%d/l%d/f%v", name, si, ci, level, favor), func(t *testing.T) {
							args := []string{"stream", "algo=hc", "level=" + strconv.Itoa(level), "sizes=" + joinSizes(sizes),
								"layout=" + sc.layout}
							if favor {
								args = append(args, "favor=1")
							}
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
							compareBlocks(t, goStreamHC(in, sizes, level, sc, favor), want)
						})
					}
				}
			}
		}
		if testing.Short() {
			break
		}
	}
}

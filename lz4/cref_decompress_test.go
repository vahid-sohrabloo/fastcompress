package lz4

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/vahid-sohrabloo/fastcompress/internal/creftest"
)

// cDecodeResult runs an lz4ref decoder mode and returns the output and C's
// result code (0, -1, -2 or -3).
func cDecodeResult(t *testing.T, blk []byte, args ...string) ([]byte, int) {
	out, err := creftest.LZ4Dec(t, blk, args...)
	if err != nil {
		code, perr := strconv.Atoi(strings.TrimPrefix(err.Error(), "ret "))
		if perr != nil {
			t.Fatalf("lz4ref: %v", err)
		}
		return nil, code
	}
	return out, 0
}

func checkDecode(t *testing.T, name string, got []byte, gerr error, want []byte, code int) {
	t.Helper()
	if code != 0 {
		if gerr == nil || errorCode(gerr) != code {
			t.Fatalf("%s: got %v (%d bytes), C %d", name, gerr, len(got), code)
		}
		return
	}
	if gerr != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s: got %v (%d bytes), C %d bytes", name, gerr, len(got), len(want))
	}
}

// mutations returns corrupted variants of a valid block.
func mutations(blk []byte, seed uint64, n int) [][]byte {
	r := rand.New(rand.NewPCG(seed, 9))
	var out [][]byte
	for i := 0; i < n && len(blk) > 0; i++ {
		b := append([]byte{}, blk...)
		switch r.IntN(4) {
		case 0:
			b[r.IntN(len(b))] ^= byte(1 << r.IntN(8))
		case 1:
			b[r.IntN(len(b))] = byte(r.Uint32())
		case 2:
			b = b[:r.IntN(len(b))]
		default:
			p := r.IntN(len(b))
			b = append(b[:p], append([]byte{0xFF, 0xFF, 0x0F}, b[p:]...)...)
		}
		out = append(out, b)
	}
	return out
}

func TestDecompressCref(t *testing.T) {
	names, m := sortedInputs(t)
	for _, name := range names {
		in := m[name]
		if len(in) > 1<<20 && testing.Short() {
			continue
		}
		blk := make([]byte, CompressBound(len(in)))
		blk = blk[:CompressFast(blk, in, 1)]
		cases := [][]byte{blk}
		nm := 40
		if len(in) > 1<<20 {
			nm = 4
		}
		cases = append(cases, mutations(blk, uint64(len(in)), nm)...)
		for i, b := range cases {
			for _, capa := range []int{len(in), len(in) + 100, max(len(in)-1, 0), len(in) / 2} {
				t.Run(fmt.Sprintf("%s/m%d/cap%d", name, i, capa), func(t *testing.T) {
					want, code := cDecodeResult(t, b, "dec", "cap="+strconv.Itoa(capa))
					dst := make([]byte, capa)
					n, err := DecompressSafe(dst, b)
					checkDecode(t, "safe", dst[:n], err, want, code)
				})
			}
			for _, target := range []int{0, 1, 100, len(in) / 3, len(in)} {
				capa := len(in) + 20
				t.Run(fmt.Sprintf("%s/m%d/partial%d", name, i, target), func(t *testing.T) {
					want, code := cDecodeResult(t, b, "decpartial", "target="+strconv.Itoa(target), "cap="+strconv.Itoa(capa))
					dst := make([]byte, capa)
					n, err := DecompressSafePartial(dst, b, target)
					checkDecode(t, "partial", dst[:n], err, want, code)
				})
			}
			if testing.Short() && i > 4 {
				break
			}
		}
	}
}

func TestDecompressDictCref(t *testing.T) {
	m := creftest.LZ4Inputs(t)
	dicts := map[string][]byte{"big": m["text-65547"], "small": m["text-65547"][:1000], "tiny": []byte("abc")}
	for _, name := range []string{"text128KiB", "text-17", "period3-200K", "random-70K"} {
		in := m[name]
		for dn, dict := range dicts {
			var s Stream
			s.LoadDict(dict)
			blk := make([]byte, CompressBound(len(in)))
			blk = blk[:s.CompressFastContinue(blk, append([]byte{}, in...), 1)]
			df := creftest.LZ4TempFile(t, dict)
			for i, b := range append([][]byte{blk}, mutations(blk, 3, 30)...) {
				for _, prefix := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/m%d/prefix%v", name, dn, i, prefix), func(t *testing.T) {
						capa := len(in)
						args := []string{"decdict", "dict=" + df, "cap=" + strconv.Itoa(capa)}
						if prefix {
							args = append(args, "prefix=1")
						}
						want, code := cDecodeResult(t, b, args...)
						var dst []byte
						var d []byte
						if prefix {
							buf := append(append([]byte{}, dict...), make([]byte, capa)...)
							d, dst = buf[:len(dict)], buf[len(dict):]
						} else {
							d, dst = dict, make([]byte, capa)
						}
						n, err := DecompressSafeUsingDict(dst, b, d)
						checkDecode(t, "dict", dst[:n], err, want, code)
					})
				}
			}
		}
	}
}

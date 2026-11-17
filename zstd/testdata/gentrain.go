//go:build ignore

// gentrain writes the training samples of train.dict and train-12k.dict
// (see README.md): 200 files of 4 KiB of synthetic text from a seeded
// generator (prose of made-up words, log lines, JSON records and key=value
// configuration), so the dictionaries contain nothing but this program's
// output.
//
//	go run zstd/testdata/gentrain.go DIR
package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run gentrain.go DIR")
		os.Exit(2)
	}
	r := rand.New(rand.NewPCG(2026, 11))
	syll := []string{"ka", "lo", "mi", "ne", "ru", "sa", "te", "vi", "zo", "pa", "qu", "ex", "an", "or", "il", "um", "dr", "st", "th", "ch"}
	words := make([]string, 1500)
	for i := range words {
		var b strings.Builder
		for range 1 + r.IntN(3) {
			b.WriteString(syll[r.IntN(len(syll))])
		}
		words[i] = b.String()
	}
	zipf := rand.NewZipf(r, 1.1, 2, uint64(len(words)-1))
	word := func() string { return words[zipf.Uint64()] }
	levels := []string{"INFO", "INFO", "INFO", "WARN", "DEBUG", "ERROR"}

	for i := range 200 {
		var b strings.Builder
		for b.Len() < 4096 {
			switch i % 4 {
			case 0: // prose
				n := 6 + r.IntN(14)
				for j := range n {
					w := word()
					if j == 0 {
						w = strings.ToUpper(w[:1]) + w[1:]
					}
					b.WriteString(w)
					if j < n-1 {
						if r.IntN(9) == 0 {
							b.WriteString(",")
						}
						b.WriteString(" ")
					}
				}
				b.WriteString([]string{". ", ". ", "? ", "!\n", ".\n\n"}[r.IntN(5)])
			case 1: // log lines
				fmt.Fprintf(&b, "2026-%02d-%02dT%02d:%02d:%02d.%03dZ %-5s service=%s req=%08x path=/api/v%d/%s/%d latency_ms=%d\n",
					1+r.IntN(12), 1+r.IntN(28), r.IntN(24), r.IntN(60), r.IntN(60), r.IntN(1000),
					levels[r.IntN(len(levels))], word(), r.Uint32(), 1+r.IntN(2), word(), r.IntN(10000), r.IntN(2000))
			case 2: // JSON records
				fmt.Fprintf(&b, `{"id":%d,"name":"%s %s","active":%t,"score":%.3f,"tags":["%s","%s"],"owner":{"user":"%s","group":%d}}`+"\n",
					r.IntN(1000000), word(), word(), r.IntN(2) == 0, r.Float64()*100, word(), word(), word(), r.IntN(64))
			case 3: // configuration
				fmt.Fprintf(&b, "[%s.%s]\n", word(), word())
				for range 2 + r.IntN(5) {
					switch r.IntN(3) {
					case 0:
						fmt.Fprintf(&b, "%s_%s = %d\n", word(), word(), r.IntN(65536))
					case 1:
						fmt.Fprintf(&b, "%s = \"%s/%s\"\n", word(), word(), word())
					default:
						fmt.Fprintf(&b, "%s_enabled = %t\n", word(), r.IntN(2) == 0)
					}
				}
				b.WriteString("\n")
			}
		}
		name := filepath.Join(os.Args[1], fmt.Sprintf("s%03d", i))
		if err := os.WriteFile(name, []byte(b.String()[:4096]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

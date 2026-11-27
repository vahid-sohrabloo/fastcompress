package huf

import (
	"math/rand/v2"
	"testing"
)

// TestRescaleStats compares rescaleStats with C's loop, one symbol at a
// time, over every symbol count and table log. Weights past nbSymbols are
// not compared: nothing reads them.
func TestRescaleStats(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 8))
	for iter := 0; iter < 200000; iter++ {
		var weight [SymbolValueMax + 1]byte
		var rank [TableLogMax + 1]uint32
		nbSymbols := uint32(rng.IntN(SymbolValueMax + 2))
		tableLog := uint32(1 + rng.IntN(TableLogMax))
		target := uint32(1 + rng.IntN(TableLogMax))
		for i := range nbSymbols {
			// weights are 0..tableLog, many of them 0
			weight[i] = byte(max(0, rng.IntN(2*int(tableLog)+1)-int(tableLog)))
		}
		for i := nbSymbols; i < uint32(len(weight)); i++ {
			weight[i] = byte(rng.IntN(16))
		}
		for i := range tableLog + 1 {
			rank[i] = rng.Uint32()
		}

		want, wantRank, wantLog := weight, rank, tableLog
		if tableLog <= target {
			wantLog = target
		}
		if tableLog < target {
			scale := target - tableLog
			for s := range nbSymbols {
				if want[s] != 0 {
					want[s] += byte(scale)
				}
			}
			for s := target; s > scale; s-- {
				wantRank[s] = wantRank[s-scale]
			}
			for s := scale; s > 0; s-- {
				wantRank[s] = 0
			}
		}

		got, gotRank := weight, rank
		gotLog := rescaleStats(got[:], gotRank[:], nbSymbols, tableLog, target)
		if gotLog != wantLog || gotRank != wantRank || string(got[:nbSymbols]) != string(want[:nbSymbols]) {
			t.Fatalf("nbSymbols %d, tableLog %d to %d: log %d want %d, ranks %v want %v\nweights %v\nwant    %v",
				nbSymbols, tableLog, target, gotLog, wantLog, gotRank, wantRank, got[:nbSymbols], want[:nbSymbols])
		}
	}
}

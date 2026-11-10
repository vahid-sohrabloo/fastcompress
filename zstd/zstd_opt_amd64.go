//go:build amd64 && !appengine && !noasm && !purego && gc

package zstd

import "github.com/vahid-sohrabloo/fastcompress/internal/cpu"

// useOptAsm selects the assembly optimal parser (baseline amd64).
var useOptAsm bool

func init() {
	cpu.RegisterAsm("opt", &useOptAsm, func() bool { return true })
	for i, c := range llCodeTable {
		llCodeBits[i] = uint16(c) | uint16(llBits[c])<<8
	}
	for i, c := range mlCodeTable {
		mlCodeBits[i] = uint16(c) | uint16(mlBits[c])<<8
	}
}

// llCodeBits and mlCodeBits hold each small length's code in the low byte
// and the code's number of extra bits in the high byte, for the assembly
// price functions (one load for both).
var (
	llCodeBits [64]uint16
	mlCodeBits [128]uint16
)

// optAsmParams holds what optParseAsm reads and returns for one block. The
// layout is fixed: zstd_opt_amd64.s reads the fields by offset (checked by
// TestOptAsmParamsLayout).
type optAsmParams struct {
	bt            *btAsmParams    // 0
	o             *optState       // 8
	opt           *optimal        // 16: the price table
	matches       *optMatch       // 24: the match table
	b             *byte           // 32: ms.window.base
	rep           *[repNum]uint32 // 40
	ip            int             // 48: in and out
	anchor        int             // 56
	iend          int             // 64
	ilimit        int             // 72
	sufficientLen uint32          // 80
	minML         uint32          // 84
	storeStart    uint32          // 88: out
	storeEnd      uint32          // 92: out
}

// optParseAsm is compressBlockOptGeneric's match loop up to the next
// segment to store; see zstd_opt_amd64.s and optParseFast.
//
//go:noescape
func optParseAsm(p *optAsmParams) (status int)

// optParseFast is compressBlockOptGeneric's match loop (noDict, from ip,
// with the statistics set by rescaleFreqs) in assembly, storing into ss and
// returning the last literals' size. It returns ok = false, doing nothing,
// when the assembly is off or a precondition fails: ms.btAsm set up for this
// block with mls 4 to 6 (btAsmSetup), dynamic prices with compressed
// literals, minMatch 3 or 4 (minML), 0 < ip, a block of at most
// blockSizeMax, sufficientLen below optNum, and the finder's preconditions
// at ip (btGetAllMatchesFast's), which then hold at every later position
// searched.
func optParseFast(ms *matchState, ss *seqStore, rep *[repNum]uint32, ip, anchor, iend int, minML, sufficientLen uint32) (int, bool) {
	o := &ms.opt
	bp := &ms.btAsm
	if !useOptAsm || !bp.ok || bp.matches != &o.matchTable[0] || bp.iLimit != iend || ip <= 0 || anchor > ip ||
		sufficientLen >= optNum || // bounds the first stage's writes (opt[1..maxML+1], maxML <= sufficientLen)
		iend-anchor > blockSizeMax || o.priceType == zopPredef || !compressedLiterals(o) || (minML != 3 && minML != 4) ||
		bp.lowestValid > uint32(ip)+bp.baseIdx || ms.nextToUpdate < bp.baseIdx {
		return 0, false
	}
	b := ms.window.base
	p := optAsmParams{
		bt:            bp,
		o:             o,
		opt:           &o.priceTable[0],
		matches:       bp.matches,
		b:             bp.b,
		rep:           rep,
		ip:            ip,
		anchor:        anchor,
		iend:          iend,
		ilimit:        iend - 8,
		sufficientLen: sufficientLen,
		minML:         minML,
	}
	opt := o.priceTable
	for {
		switch optParseAsm(&p) {
		case 0:
			ms.asmLoopRuns++
			return iend - anchor, true
		case 2:
			panic("zstd: binary tree holds an index at or after the current position")
		}
		// save sequences
		for storePos := p.storeStart; storePos <= p.storeEnd; storePos++ {
			llen := opt[storePos].litlen
			mlen := opt[storePos].mlen
			offBase := opt[storePos].off
			advance := llen + mlen

			if mlen == 0 { // only literals => must be last "sequence", actually starting a new stream of sequences
				ip = anchor + int(llen) // last "sequence" is a bunch of literals => don't progress anchor
				continue                // will finish
			}

			updateStats(o, llen, b[anchor:], offBase, mlen)
			storeSeq(ss, int(llen), b[anchor:], offBase, int(mlen))
			anchor += int(advance)
			ip = anchor
		}

		// update all costs
		setBasePrices(o)
		p.ip, p.anchor = ip, anchor
	}
}

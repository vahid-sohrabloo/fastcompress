# Task 20d: profiles and A/Bs

Harnesses as in Task 20c (2026-10-30-l3-smallblock-profile.md): `charn`
(C, libzstd 1.5.7 `gcc -O3`) and `goharn` (EncodeAll, CRC off), one reused
context, one call per block, `perf stat` `cpu_core/` user-mode counts per
input byte, `taskset -c 2`. A/B figures are medians of per-round paired cycle
ratios (`ab.sh`, 5 rounds, interquartile range in brackets). `tar32m` is the
first 32 MiB of silesia.tar. Background load (clickhouse, chrome) was 1.5-5
during the A/Bs; the paired ranges were tight.

## Step 1: hash-chain finder (L7 at 16 KiB blocks)

Before (93ea0fb), L7 tar32m -B16K:

| side | cycles/B | instr/B | branches/B | br-miss/KiB |
|---|---|---|---|---|
| C | 60.7 | 140.2 | 23.0 | 714 |
| Go | 91.8 | 392.2 | 62.9 | 742 |

`perf record` (Go): hcFindBestMatch 54%, insertAndFindFirstIndexInternal
23%, compressBlockLazyGeneric 13%, searchMax 1%: 91% in the pure-Go lazy
parser and hash-chain finder (windowLog 14 turns the row finder off).

Fix: the whole lazy loop in assembly with the hash-chain finder, generated
per mls (lazyHc4/5/6 + hcSearch4/5/6, the row lazy parser's template with
the finder swapped).

| config | Go before | Go after | change | C |
|---|---|---|---|---|
| L7 -B16K cycles/B | 91.79 | 60.84 | **-33.8%** [-33.8..-33.7] | 60.62 |
| L7 -B16K instr/B | 392.2 | 126.6 | | 140.2 |
| L7 -B128K cycles/B (row finder, unchanged code) | 44.15 | 43.38 | -1.9% | 44.70 |

## Step 2: L1/L3 whole files

Before (3ebe545), per input byte:

| config | C cycles/B | Go cycles/B | C instr/B | Go instr/B | C br/B | Go br/B | C br-miss/KiB | Go br-miss/KiB |
|---|---|---|---|---|---|---|---|---|
| L1 dickens | 10.76 | 11.82 | 33.7 | 33.8 | 3.33 | 5.01 | 102 | 108 |
| L1 mozilla | 7.32 | 8.08 | 24.3 | 25.6 | 2.29 | 3.75 | 68 | 75 |
| L1 sao | 8.80 | 9.52 | 37.0 | 37.5 | 3.76 | 5.76 | 48 | 54 |
| L3 dickens | 19.04 | 19.83 | 39.6 | 41.0 | 3.40 | 5.33 | 129 | 130 |
| L3 samba | 9.64 | 10.51 | 21.4 | 23.2 | 1.92 | 3.26 | 78 | 77 |
| L3 xml | 6.21 | 6.62 | 14.5 | 15.5 | 1.35 | 2.17 | 61 | 58 |
| L3 mozilla | 12.44 | 13.40 | 31.6 | 35.5 | 2.82 | 5.47 | 91 | 92 |

`perf record`, L1 dickens: C's ZSTD_compressBlock_fast is 72.9% (7.84
cycles/B); Go's fast asm + fastCount + fastCopyLits + stale loop is 73.0%
(8.62 cycles/B), so +0.78 of the +1.06 cycles/B gap is the loop and its two
helpers (fastCount 7.6%, fastCopyLits 2.7%). The rest is spread over the
entropy stage (encodeSequences 1.26 vs 1.20). L3 mozilla: dfast asm (both
loops) 62.7% + fastCount 5.7% + fastCopyLits 2.0% vs C's 70.1%.

Tried (L1, paired A/B vs 3ebe545, 5 rounds):

| variant | dickens | mozilla | sao | xml | samba |
|---|---|---|---|---|---|
| fastCount's first 8 bytes inline, then the call | -0.9..-1.4% | -0.3% | -0.5% | **+1.7%** | **+1.0%** |
| fastCount's whole body inline (no call) | -0.4% | +0.4% | +0.1% | +0.2% | +0.3% |
| literals <= 16: one inline 16-byte copy | **-1.7%** | **-2.2%** | +0.4% | **-1.9%** | **-1.6%** |
| inline 16-byte wildcopy loop for every length | -1.5% | -1.8% | +0.2% | -1.9% | -1.5% |

The call to fastCount is not the cost (inlining it gains nothing, and the
first-8 split costs xml and samba); the literal copy is. Kept: the <= 16
copy16 (C's ZSTD_storeSeq fast path), in the fast and the double-fast loop.
The literal buffer now has C's WILDCOPY_OVERLENGTH (32) bytes of slack; the
wrappers require 16 of them, and the source read is checked against iend.

L3 with the same change (paired, 5 rounds): dickens -0.2%, mozilla -1.3%,
sao -0.4%, xml -0.2%, samba -0.1%, ooffice -1.6%, tar -B128K -0.9%,
tar -B16K -0.8%. L1: ooffice -0.9%, tar -B16K -1.3%, x-ray +0.1%.

## Step 3: L11 (btopt at 16 KiB, btlazy2 at 128 KiB)

Before (6406544), tar4m (first 4 MiB of silesia.tar):

| config | side | cycles/B | instr/B | br/B | br-miss/KiB |
|---|---|---|---|---|---|
| L11 -B16K (btopt, mls 4) | C | 210.6 | 525.8 | 60.5 | 3501 |
| | Go | 288.5 | 1028.0 | 152.8 | 3556 |
| L11 -B128K (btlazy2) | C | 226.9 | 336.0 | 43.7 | 3695 |
| | Go | 252.2 | 484.7 | 81.9 | 3704 |

`perf record` cycles, -B16K:
- C: btGetAllMatches_noDict_4 40.8%, compressBlock_opt0 40.1%, insertBt1 13.9%.
- Go: insertBtAndGetAllMatches 44.8%, compressBlockOptGeneric 20.9%,
  insertBt1 13.4%, getMatchPrice 6.3%, litLengthPrice 4.8%,
  btGetAllMatches 1.9%, updateTreeInternal 1.6%, llIncPrice 1.3%.

The gap is instructions (2x C at IPC 3.6), not misses: branch and L1D
misses per byte equal C's.

### insertBt1's tree walk in assembly

The walk of insertBt1 (C: ZSTD_insertBt1) as insertBt1Asm, beside the
btlazy2 tree walks in dubt_*.s. L11 -B16K -2.3% [-3.0..-1.2] (instr/B
1028 -> 1004), -B128K -0.4%.

Tried and dropped: insertBtAndGetAllMatches' tree walk alone in assembly
(called from the Go function): -1.1% [-1.4..+1.0]. An instructions
profile showed why: the Go code around the walk (repcode checks, hash,
setup, the call) ran about 280 instructions per call, more than the walk
itself (170); C's whole btGetAllMatches_noDict_4 is 238 per byte.

### insertBtAndGetAllMatches (noDict, mls 4-6) in assembly

The whole function (repcode checks, hash, tree walk; C:
ZSTD_insertBtAndGetAllMatches) as btGetAllNoDictAsm, with its per-block
parameters in ms.btAsm (set up by compressBlockOptGeneric for noDict) so a
call passes only ip, rep, ll0 and lengthToBeat. mls 3 (hash3 table) stays
in Go.

L11 tar4m -B16K vs the insertBt1 commit: **-12.4%** [-12.4..-12.3] cycles
(284.8 -> 249.5; C 209.8), instr/B 1004 -> 718 (C 526).

Profile after (cycles): btGetAllNoDictAsm 34.8%, compressBlockOptGeneric
26.0%, insertBt1Asm 8.8%, getMatchPrice 7.4%, litLengthPrice 6.2%,
insertBt1 (Go part) 2.8%.

### Price hoisting in the optimal parser

With dynamic statistics, getMatchPrice is an offset part plus a match
length part (uint32 sums); the parser's two price loops now compute the
offset part once per match and inline the length part, and take
llPrice(0) once per forward pass (the statistics only change between
passes). L11 tar4m -B16K: -1.2% [-1.2..-0.8] (instr/B 718 -> 704).

Tried and dropped: setting the next repcodes element by element (instead of
newRep's array copy) after a perf annotate put 9.5% of the parser's cycles
on that line: +0.6% against the hoisting alone (the time is the load
latency of opt[prev], not the copy).

### btlazy2: updateDUBT + dubtFindBestMatch in assembly (L11 -B128K)

Profile before (faf2d5b), L11 tar4m -B128K, share of instructions / cycles:
dubtFindBestMatch (Go) 24.5% / 18.9%, insertDUBT1Asm 24.4% / 31.3%,
dubtSearchAsm 22.4% / 33.3%, updateDUBT (Go) 13.2% / 5.4%,
compressBlockLazyGeneric 6.7% / 4.1%, the Go wrappers dubtSearch and
insertDUBT1 3.8% / 4.0%. Go ran 485 instr/B against C's 336.

dubtFindBestMatchAsm does updateDUBT, the unsorted-chain walk and the batch
sort, calling the existing insertDUBT1Asm and dubtSearchAsm loops with
stack arguments; its per-block parameters are ms.btAsm (as for btopt).

L11 tar4m -B128K: **-7.2%** [-7.3..-7.2] (252.4 -> 234.3 cycles/B, C 226.9),
instr/B 485 -> 374. -B16K (btopt, untouched): +0.4%.

### llIncPrice without litLengthPrice calls

litLengthPrice (not inlined) was 6.2% of L11 -B16K cycles, called twice per
parser position by llIncPrice. With dynamic statistics and a literal
length below 64, llIncPrice now reads both codes from llCodeTable and
returns 0 when they are equal; otherwise the difference of the two
bits*256 - bitWeight(freq) terms (the base price cancels and the uint32
prices cannot wrap). L11 tar4m -B16K: -2.7% [-2.8..-2.5] (instr/B 706 -> 677).

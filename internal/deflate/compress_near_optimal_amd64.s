//go:build amd64 && !appengine && !noasm && !purego && gc

// The backward pass of deflate_find_min_cost_path (libdeflate v1.26
// lib/deflate_compress.c, Copyright 2016 Eric Biggers, MIT; see NOTICE): for
// each position from the end of the block to its start, the cheapest of the
// literal and every (length, smallest offset) of the cached matches, under
// the current cost model. Semantics are those of the Go loop of
// Compressor.findMinCostPath (the Go reference, statement for statement).
//
// Preemption: the routine is NOSPLIT assembly, so the goroutine is not
// asynchronously preemptible while inside it. Bound per call: cur - stop
// positions, which findMinCostPath keeps at most minCostPathMaxPositions
// (1024), each with at most MAX_MATCHES_PER_POS matches and
// DEFLATE_MAX_MATCH_LEN - 2 lengths.

#include "textflag.h"

// nearOptimalState byte offsets (asserted in compress_near_optimal_amd64.go):
// match_cache, optimum_nodes and its last entry (index MAX_BLOCK_LENGTH),
// costs.literal, costs.length and costs.offset_slot.
#define CACHE_OFF 524288
#define NODES_OFF 6526340
#define NODES_LAST 8966332
#define NODES_COUNT_M1 304999
#define COST_LIT 8966340
#define COST_LEN 8967364
#define COST_OFFSLOT 8968400

// func minCostPathAsm(ns *nearOptimalState, slotFull *[deflateMaxMatchOffset + 1]uint8,
//	cur, cp, stop int) int
//
// From optimum node cur (whose cost_to_end is set) down to node stop: for
// each position cur-1, ..., stop the match cache entries before cp (the
// header {num_matches, literal}, then the num_matches matches in increasing
// length) give the position's cost_to_end and item; returns the cache index
// of position stop's first entry (its first match, else its header), the cp
// the next call continues from. The length loop uses
// two CMOVs for the "cost_to_end < best_cost_to_end" update, as the Go loop
// compiles to (C's branch inside the loop; same results).
// Registers: DI ns, R8 slotFull, SI &optimum_nodes[cur] (cur_node), R14
// &optimum_nodes[stop], BX the cache pointer (cache_ptr), R11
// best_cost_to_end, R12 its item, R10 the match pointer, AX len, CX offset
// << OPTIMUM_OFFSET_SHIFT, DX the match length, R9 offset_cost, R13 / R15
// scratch (cost_to_end, item); frame: the cache base and the first match
// of the position.
//
// BOUNDS: preconditions (kept by findMinCostPath): 0 <= stop < cur <=
// MAX_BLOCK_LENGTH (optimum_nodes has MAX_BLOCK_LENGTH + 1 entries; checked
// once per call, unsigned, so a negative stop fails too: overrun); 0 <= cp
// <= len(match_cache). Memory accesses:
// * optimum_nodes[i] for stop <= i <= cur (cost_to_end and item written for
//   i < cur, cost_to_end of i + 1 read) and optimum_nodes[i + len] (read) with
//   len <= the match's length, checked per match against &optimum_nodes
//   [MAX_BLOCK_LENGTH] (NODES_LAST).
// * match_cache: the cache pointer moves down from &match_cache[cp]; each
//   header is read only after the check that it is >= &match_cache[0], the
//   matches of a position only after the check that the first is too, all
//   below the header (so below &match_cache[cp]).
// * costs.literal[literal & 0xFF]; costs.length[len] with 3 <= len <= the
//   match's length <= 258 (both checked per match: the length must be >= len,
//   which holds for valid caches, where the lengths of a position strictly
//   increase from >= 3, and <= 258); slotFull[offset] with offset <= 32768
//   (checked per match); costs.offset_slot[slotFull[offset] & 0x1F] (32
//   entries).
// * A failed check goes to overrun: a 1-byte store to address 0 (a fault by
//   design, as in flush_block_amd64.s), never memory.
// * The 24-byte frame.
// C: deflate_find_min_cost_path (the loop before the tally)
TEXT ·minCostPathAsm(SB), NOSPLIT, $24-48
	MOVQ ns+0(FP), DI
	MOVQ slotFull+8(FP), R8
	MOVQ cur+16(FP), AX
	MOVQ cp+24(FP), BX
	MOVQ stop+32(FP), CX
	CMPQ AX, $NODES_COUNT_M1
	JHI  overrun              // cur > MAX_BLOCK_LENGTH
	CMPQ CX, AX
	JCC  overrun              // stop >= cur (unsigned)
	LEAQ NODES_OFF(DI)(AX*8), SI
	LEAQ NODES_OFF(DI)(CX*8), R14
	LEAQ CACHE_OFF(DI), CX
	MOVQ CX, cbase-8(SP)
	LEAQ (CX)(BX*4), BX

pos:
	SUBQ    $8, SI            // cur_node--
	SUBQ    $4, BX            // cache_ptr--
	CMPQ    BX, cbase-8(SP)
	JB      overrun
	MOVWLZX (BX), DX          // num_matches
	MOVWLZX 2(BX), CX         // literal
	MOVL    8(SI), R11
	MOVL    CX, R12
	ANDL    $0xFF, CX
	ADDL    COST_LIT(DI)(CX*4), R11 // best_cost_to_end
	SHLL    $9, R12
	ORL     $1, R12           // item = (literal << OPTIMUM_OFFSET_SHIFT) | 1
	TESTL   DX, DX
	JZ      store
	SHLQ    $2, DX
	MOVQ    BX, R10
	SUBQ    DX, R10           // match = cache_ptr - num_matches
	CMPQ    R10, cbase-8(SP)
	JB      overrun
	MOVQ    R10, mstart-16(SP)
	MOVL    $3, AX            // len = DEFLATE_MIN_MATCH_LEN

match:
	MOVWLZX 2(R10), CX        // offset
	MOVWLZX (R10), DX         // match->length
	CMPL    CX, $32768
	JA      overrun
	CMPL    DX, AX
	JB      overrun
	CMPL    DX, $258
	JA      overrun
	LEAQ    (SI)(DX*8), R9
	LEAQ    NODES_LAST(DI), R13
	CMPQ    R9, R13
	JA      overrun
	MOVBLZX (R8)(CX*1), R9    // offset_slot_full[offset]
	ANDL    $0x1F, R9
	MOVL    COST_OFFSLOT(DI)(R9*4), R9 // offset_cost
	SHLL    $9, CX            // offset << OPTIMUM_OFFSET_SHIFT

lenloop:
	MOVL    COST_LEN(DI)(AX*4), R13
	ADDL    R9, R13
	ADDL    (SI)(AX*8), R13   // cost_to_end
	LEAL    (CX)(AX*1), R15
	CMPL    R13, R11
	CMOVLCS R13, R11          // cost_to_end < best_cost_to_end
	CMOVLCS R15, R12
	INCL    AX
	CMPL    AX, DX
	JLS     lenloop           // ++len <= match->length

	ADDQ $4, R10
	CMPQ R10, BX
	JNE  match
	MOVQ mstart-16(SP), BX    // cache_ptr -= num_matches

store:
	MOVL R11, (SI)
	MOVL R12, 4(SI)
	CMPQ SI, R14
	JNE  pos

	MOVQ cbase-8(SP), CX
	SUBQ CX, BX
	SHRQ $2, BX
	MOVQ BX, ret+40(FP)
	RET

overrun:
	MOVQ $0, DX
	MOVB AX, (DX)
	JMP  overrun

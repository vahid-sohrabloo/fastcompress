//go:build arm64 && !appengine && !noasm && !purego && gc

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

// nearOptimalState byte offsets (asserted in compress_near_optimal_arm64.go):
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
// the next call continues from. compress_near_optimal_amd64.s step for step,
// with two CSELs for the "cost_to_end < best_cost_to_end" update.
// Registers: R0 ns, R1 slotFull, R2 &optimum_nodes[cur] (cur_node), R3
// &optimum_nodes[stop], R4 the cache pointer (cache_ptr), R5
// best_cost_to_end, R6 its item, R7 the match pointer, R8 len, R9 offset
// << OPTIMUM_OFFSET_SHIFT, R10 the match length, R11 offset_cost, R12 /
// R13 scratch (cost_to_end, item), R14 the cache base, R15 the first match
// of the position, R16 / R17 / R19 costs.literal / length / offset_slot,
// R20 &optimum_nodes[MAX_BLOCK_LENGTH].
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
// * No frame.
// C: deflate_find_min_cost_path (the loop before the tally)
TEXT ·minCostPathAsm(SB), NOSPLIT, $0-48
	MOVD ns+0(FP), R0
	MOVD slotFull+8(FP), R1
	MOVD cur+16(FP), R8
	MOVD cp+24(FP), R4
	MOVD stop+32(FP), R9
	MOVD $NODES_COUNT_M1, R12
	CMP  R12, R8
	BHI  overrun                 // cur > MAX_BLOCK_LENGTH
	CMP  R8, R9
	BHS  overrun                 // stop >= cur (unsigned)
	MOVD $NODES_OFF, R12
	ADD  R0, R12, R12
	ADD  R8<<3, R12, R2
	ADD  R9<<3, R12, R3
	ADD  $CACHE_OFF, R0, R14
	ADD  R4<<2, R14, R4
	MOVD $COST_LIT, R16
	ADD  R0, R16, R16
	MOVD $COST_LEN, R17
	ADD  R0, R17, R17
	MOVD $COST_OFFSLOT, R19
	ADD  R0, R19, R19
	MOVD $NODES_LAST, R20
	ADD  R0, R20, R20

pos:
	SUB   $8, R2, R2             // cur_node--
	SUB   $4, R4, R4             // cache_ptr--
	CMP   R14, R4
	BLO   overrun
	MOVHU (R4), R10              // num_matches
	MOVHU 2(R4), R9              // literal
	MOVWU 8(R2), R5
	MOVD  R9, R6
	ANDW  $0xFF, R9, R9
	MOVWU (R16)(R9<<2), R12
	ADDW  R12, R5, R5            // best_cost_to_end
	LSLW  $9, R6, R6
	ORRW  $1, R6, R6             // item = (literal << OPTIMUM_OFFSET_SHIFT) | 1
	CBZW  R10, store
	SUB   R10<<2, R4, R7         // match = cache_ptr - num_matches
	CMP   R14, R7
	BLO   overrun
	MOVD  R7, R15
	MOVD  $3, R8                 // len = DEFLATE_MIN_MATCH_LEN

match:
	MOVHU 2(R7), R9              // offset
	MOVHU (R7), R10              // match->length
	CMPW  $32768, R9
	BHI   overrun
	CMPW  R8, R10
	BLO   overrun
	CMPW  $258, R10
	BHI   overrun
	ADD   R10<<3, R2, R11
	CMP   R20, R11
	BHI   overrun
	MOVBU (R1)(R9), R11          // offset_slot_full[offset]
	ANDW  $0x1F, R11, R11
	MOVWU (R19)(R11<<2), R11     // offset_cost
	LSLW  $9, R9, R9             // offset << OPTIMUM_OFFSET_SHIFT

lenloop:
	MOVWU (R17)(R8<<2), R12
	ADDW  R11, R12, R12
	ADD   R8<<3, R2, R13
	MOVWU (R13), R13
	ADDW  R13, R12, R12          // cost_to_end
	ADDW  R8, R9, R13
	CMPW  R5, R12
	CSELW LO, R12, R5, R5        // cost_to_end < best_cost_to_end
	CSELW LO, R13, R6, R6
	ADDW  $1, R8, R8
	CMPW  R10, R8
	BLS   lenloop                // ++len <= match->length

	ADD  $4, R7, R7
	CMP  R4, R7
	BNE  match
	MOVD R15, R4                 // cache_ptr -= num_matches

store:
	MOVW R5, (R2)
	MOVW R6, 4(R2)
	CMP  R3, R2
	BNE  pos

	SUB  R14, R4, R4
	LSR  $2, R4, R4
	MOVD R4, ret+40(FP)
	RET

overrun:
	MOVD ZR, R12
	MOVB R8, (R12)
	JMP  overrun

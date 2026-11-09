//go:build amd64 && !appengine && !noasm && !purego && gc

// matchfinder_init and matchfinder_rebase of libdeflate v1.26
// lib/x86/matchfinder_impl.h (Copyright 2016 Eric Biggers, MIT; see NOTICE),
// the SSE2 versions (HAVE_SSE2_NATIVE: what an x86-64 build without -mavx2
// compiles): four 16-byte vectors (32 mf_pos_t entries) per iteration. C's
// size is a multiple of MATCHFINDER_SIZE_ALIGNMENT; here the entry count is
// rounded down to a multiple of 32 and the Go caller does the rest.

#include "textflag.h"

// func matchfinderInitSSE2(data []mfPos)
//
// BOUNDS: writes data[0 : len(data)&^31] (2-byte entries, bytes
// [base, base + 2*(len&^31))) and nothing else: CX = len >> 5 iterations of
// 64 bytes from DI = base; no read of data. len(data) < 32 writes nothing.
// Unaligned stores (MOVOU): Go guarantees only 2-byte alignment.
// C: matchfinder_init_sse2
TEXT ·matchfinderInitSSE2(SB), NOSPLIT, $0-24
	MOVQ data_base+0(FP), DI
	MOVQ data_len+8(FP), CX
	SHRQ $5, CX
	JZ   initdone

	// X0 = MATCHFINDER_INITVAL (0x8000) in each 16-bit lane.
	MOVL   $0x80008000, AX
	MOVQ   AX, X0
	PSHUFD $0, X0, X0

initloop:
	MOVOU X0, (DI)
	MOVOU X0, 16(DI)
	MOVOU X0, 32(DI)
	MOVOU X0, 48(DI)
	ADDQ  $64, DI
	DECQ  CX
	JNZ   initloop

initdone:
	RET

// func matchfinderRebaseSSE2(data []mfPos)
//
// BOUNDS: reads and writes data[0 : len(data)&^31] (bytes
// [base, base + 2*(len&^31))) and nothing else: CX = len >> 5 iterations,
// each loading and storing the same 64 bytes at DI = base + 64*i. len(data)
// < 32 touches nothing. Unaligned accesses (MOVOU).
// C: matchfinder_rebase_sse2 (_mm_adds_epi16 with
// -MATCHFINDER_WINDOW_SIZE)
TEXT ·matchfinderRebaseSSE2(SB), NOSPLIT, $0-24
	MOVQ data_base+0(FP), DI
	MOVQ data_len+8(FP), CX
	SHRQ $5, CX
	JZ   rebasedone

	// X4 = (u16)-MATCHFINDER_WINDOW_SIZE (0x8000) in each 16-bit lane.
	MOVL   $0x80008000, AX
	MOVQ   AX, X4
	PSHUFD $0, X4, X4

rebaseloop:
	MOVOU  (DI), X0
	MOVOU  16(DI), X1
	MOVOU  32(DI), X2
	MOVOU  48(DI), X3
	PADDSW X4, X0
	PADDSW X4, X1
	PADDSW X4, X2
	PADDSW X4, X3
	MOVOU  X0, (DI)
	MOVOU  X1, 16(DI)
	MOVOU  X2, 32(DI)
	MOVOU  X3, 48(DI)
	ADDQ   $64, DI
	DECQ   CX
	JNZ    rebaseloop

rebasedone:
	RET

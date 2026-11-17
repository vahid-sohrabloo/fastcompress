//go:build arm64 && !appengine && !noasm && !purego && gc

// matchfinder_init and matchfinder_rebase of libdeflate v1.26
// lib/arm/matchfinder_impl.h (Copyright 2016 Eric Biggers, MIT; see NOTICE),
// the NEON versions: four 16-byte vectors (32 mf_pos_t entries) per
// iteration. C's size is a multiple of MATCHFINDER_SIZE_ALIGNMENT; here the
// entry count is rounded down to a multiple of 32 and the Go caller does the
// rest.

#include "textflag.h"

// func matchfinderInitSSE2(data []mfPos)
//
// BOUNDS: writes data[0 : len(data)&^31] (2-byte entries, bytes
// [base, base + 2*(len&^31))) and nothing else: R1 = len >> 5 iterations of
// 64 bytes from R0 = base; no read of data. len(data) < 32 writes nothing.
// C: matchfinder_init_neon
TEXT ·matchfinderInitSSE2(SB), NOSPLIT, $0-24
	MOVD data_base+0(FP), R0
	MOVD data_len+8(FP), R1
	LSR  $5, R1, R1
	CBZ  R1, initdone
	MOVD $0x8000, R2
	VDUP R2, V0.H8               // MATCHFINDER_INITVAL in each 16-bit lane
	VMOV V0.B16, V1.B16
	VMOV V0.B16, V2.B16
	VMOV V0.B16, V3.B16

initloop:
	VST1.P [V0.H8, V1.H8, V2.H8, V3.H8], 64(R0)
	SUBS   $1, R1, R1
	BNE    initloop

initdone:
	RET

// func matchfinderRebaseSSE2(data []mfPos)
//
// BOUNDS: reads and writes data[0 : len(data)&^31] (bytes
// [base, base + 2*(len&^31))) and nothing else: R1 = len >> 5 iterations,
// each loading and storing the same 64 bytes at R0 = base + 64*i. len(data)
// < 32 touches nothing.
// C: matchfinder_rebase_neon (vqsubq_s16 of MATCHFINDER_WINDOW_SIZE, done
// on the bits as umin(x, 0x8000) | 0x8000)
TEXT ·matchfinderRebaseSSE2(SB), NOSPLIT, $0-24
	MOVD data_base+0(FP), R0
	MOVD data_len+8(FP), R1
	LSR  $5, R1, R1
	CBZ  R1, rebasedone
	MOVD $0x8000, R2
	VDUP R2, V5.H8

rebaseloop:
	VLD1  (R0), [V0.H8, V1.H8, V2.H8, V3.H8]
	VUMIN V5.H8, V0.H8, V0.H8
	VUMIN V5.H8, V1.H8, V1.H8
	VUMIN V5.H8, V2.H8, V2.H8
	VUMIN V5.H8, V3.H8, V3.H8
	VORR  V5.B16, V0.B16, V0.B16
	VORR  V5.B16, V1.B16, V1.B16
	VORR  V5.B16, V2.B16, V2.B16
	VORR  V5.B16, V3.B16, V3.B16
	VST1.P [V0.H8, V1.H8, V2.H8, V3.H8], 64(R0)
	SUBS  $1, R1, R1
	BNE   rebaseloop

rebasedone:
	RET

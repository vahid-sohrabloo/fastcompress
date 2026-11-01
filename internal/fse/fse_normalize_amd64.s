//go:build amd64 && !appengine && !noasm && !purego && gc

// normalizeCountAsm is gcc's FSE_normalizeCount from ref/zstd/programs/zstd
// (zstd v1.5.7, 0xf890-0xfc40), FSE_normalizeM2 inlined as gcc inlined it,
// transliterated instruction for instruction by Task 20k. Each line keeps
// C's address and instruction in its comment. Registers are C's (rax = AX,
// ..., r15 = R15); C's red-zone slots at -0x18..-0x4(%rsp) are at
// 0x28..0x3c(SP) (C's offset + 0x40).
//
// Departures from gcc's code: no endbr64 or register pushes (the registers
// are scratch in ABI0), the arguments come from the Go frame, and each retq
// stores RAX to ret.
//
// Go arguments (FP offsets, checked by go vet's asmdecl): norm+0,
// tableLog+8, count+16, total+24, maxSymbol+32, useLowProbCount+40, ret+48.

#include "textflag.h"

// FSE_normalizeCount's rtbTable.
DATA fseRtbTable<>+0(SB)/4, $0
DATA fseRtbTable<>+4(SB)/4, $473195
DATA fseRtbTable<>+8(SB)/4, $504333
DATA fseRtbTable<>+12(SB)/4, $520860
DATA fseRtbTable<>+16(SB)/4, $550000
DATA fseRtbTable<>+20(SB)/4, $700000
DATA fseRtbTable<>+24(SB)/4, $750000
DATA fseRtbTable<>+28(SB)/4, $830000
GLOBL fseRtbTable<>(SB), RODATA|NOPTR, $32

TEXT ·normalizeCountAsm(SB), NOSPLIT, $64-56
	MOVQ norm+0(FP), DI
	MOVQ tableLog+8(FP), SI
	MOVQ count+16(FP), DX
	MOVQ total+24(FP), CX
	MOVQ maxSymbol+32(FP), R8
	MOVQ useLowProbCount+40(FP), R9
	MOVQ DI, R10                             // f896 movq   %rdi,%r10
	MOVQ DX, R11                             // f899 movq   %rdx,%r11
	MOVQ CX, DI                              // f89c movq   %rcx,%rdi
	MOVL SI, 0x30(SP)                        // f8a7 movl   %esi,-0x10(%rsp)
	TESTL SI, SI                             // f8ab testl  %esi,%esi
	JEQ Nf8d8                                // f8ad je     f8d8 <FSE_normalizeCount+0x48>
	CMPL SI, $4                              // f8af cmpl   $0x4,%esi
	JLS Nfa20                                // f8b2 jbe    fa20 <FSE_normalizeCount+0x190>
	CMPL 0x30(SP), $12                       // f8b8 cmpl   $0xc,-0x10(%rsp)
	MOVQ $-44, AX                            // f8bd movq   $0xffffffffffffffd4,%rax
	JLS Nf8e0                                // f8c4 jbe    f8e0 <FSE_normalizeCount+0x50>
	MOVQ AX, ret+48(FP)
	RET                                      // f8d0 retq
	// f8d1 nopl   0x0(%rax)

Nf8d8:
	MOVL $11, 0x30(SP)                       // f8d8 movl   $0xb,-0x10(%rsp)

Nf8e0:
	BSRL DI, AX                              // f8e0 bsrl   %edi,%eax
	BSRL R8, DX                              // f8e3 bsrl   %r8d,%edx
	MOVL 0x30(SP), BX                        // f8e7 movl   -0x10(%rsp),%ebx
	ADDL $1, AX                              // f8eb addl   $0x1,%eax
	ADDL $2, DX                              // f8ee addl   $0x2,%edx
	CMPL AX, DX                              // f8f1 cmpl   %edx,%eax
	CMOVLHI DX, AX                           // f8f3 cmoval %edx,%eax
	CMPL BX, AX                              // f8f6 cmpl   %eax,%ebx
	JCS Nfa20                                // f8f8 jb     fa20 <FSE_normalizeCount+0x190>
	CMPL R9, $1                              // f8fe cmpl   $0x1,%r9d
	MOVL DI, CX                              // f902 movl   %edi,%ecx
	MOVQ $0x4000000000000000, AX             // f904 movabsq $0x4000000000000000,%rax
	MOVL $62, R12                            // f90e movl   $0x3e,%r12d
	SBBL BP, BP                              // f914 sbbl   %ebp,%ebp
	XORL DX, DX                              // f916 xorl   %edx,%edx
	MOVL $0, 0x34(SP)                        // f918 movl   $0x0,-0xc(%rsp)
	SUBL BX, R12                             // f920 subl   %ebx,%r12d
	DIVQ CX                                  // f923 divq   %rcx
	MOVL BX, CX                              // f926 movl   %ebx,%ecx
	ANDL $2, BP                              // f928 andl   $0x2,%ebp
	XORL R15, R15                            // f92b xorl   %r15d,%r15d
	XORL DX, DX                              // f92e xorl   %edx,%edx
	SUBL $1, BP                              // f930 subl   $0x1,%ebp
	MOVQ AX, 0x28(SP)                        // f933 movq   %rax,-0x18(%rsp)
	MOVL $42, AX                             // f938 movl   $0x2a,%eax
	SUBL BX, AX                              // f93d subl   %ebx,%eax
	MOVL AX, 0x38(SP)                        // f93f movl   %eax,-0x8(%rsp)
	MOVL $1, AX                              // f943 movl   $0x1,%eax
	SHLL CX, AX                              // f948 shll   %cl,%eax
	MOVL AX, 0x3c(SP)                        // f94a movl   %eax,-0x4(%rsp)
	MOVL AX, R13                             // f94e movl   %eax,%r13d
	MOVQ DI, AX                              // f951 movq   %rdi,%rax
	SHRQ CX, AX                              // f954 shrq   %cl,%rax
	MOVQ AX, R14                             // f957 movq   %rax,%r14
	JMP Nf972                                // f95a jmp    f972 <FSE_normalizeCount+0xe2>
	// f95c nopl   0x0(%rax)

Nf960:
	SUBL $1, R13                             // f960 subl   $0x1,%r13d
	MOVL BP, AX                              // f964 movl   %ebp,%eax

Nf966:
	ADDL $1, DX                              // f966 addl   $0x1,%edx
	MOVW AX, (R9)                            // f969 movw   %ax,(%r9)
	CMPL R8, DX                              // f96d cmpl   %edx,%r8d
	JCS Nf9ec                                // f970 jb     f9ec <FSE_normalizeCount+0x15c>

Nf972:
	MOVL DX, AX                              // f972 movl   %edx,%eax
	MOVL (R11)(AX*4), SI                     // f974 movl   (%r11,%rax,4),%esi
	CMPQ SI, DI                              // f978 cmpq   %rdi,%rsi
	JEQ Nfa38                                // f97b je     fa38 <FSE_normalizeCount+0x1a8>
	LEAQ (R10)(AX*2), R9                     // f981 leaq   (%r10,%rax,2),%r9
	XORL AX, AX                              // f985 xorl   %eax,%eax
	TESTL SI, SI                             // f987 testl  %esi,%esi
	JEQ Nf966                                // f989 je     f966 <FSE_normalizeCount+0xd6>
	CMPL R14, SI                             // f98b cmpl   %esi,%r14d
	JCC Nf960                                // f98e jae    f960 <FSE_normalizeCount+0xd0>
	MOVQ 0x28(SP), AX                        // f990 movq   -0x18(%rsp),%rax
	MOVL R12, CX                             // f995 movl   %r12d,%ecx
	IMULQ AX, SI                             // f998 imulq  %rax,%rsi
	MOVQ SI, AX                              // f99c movq   %rsi,%rax
	SHRQ CX, AX                              // f99f shrq   %cl,%rax
	CMPQ AX, $7                              // f9a2 cmpq   $0x7,%rax
	JHI Nf9cd                                // f9a6 ja     f9cd <FSE_normalizeCount+0x13d>
	MOVQ AX, BX                              // f9a8 movq   %rax,%rbx
	SHLQ CX, BX                              // f9ab shlq   %cl,%rbx
	MOVLQSX AX, CX                           // f9ae movslq %eax,%rcx
	SUBQ BX, SI                              // f9b1 subq   %rbx,%rsi
	LEAQ fseRtbTable<>(SB), BX               // f9b4 leaq   rtbTable(%rip),%rbx
	MOVL (BX)(CX*4), BX                      // f9bb movl   (%rbx,%rcx,4),%ebx
	MOVBLZX 0x38(SP), CX                     // f9be movzbl -0x8(%rsp),%ecx
	SHLQ CX, BX                              // f9c3 shlq   %cl,%rbx
	CMPQ BX, SI                              // f9c6 cmpq   %rsi,%rbx
	ADCW $0, AX                              // f9c9 adcw   $0x0,%ax

Nf9cd:
	CMPW AX, R15                             // f9cd cmpw   %r15w,%ax
	JLE Nf9da                                // f9d1 jle    f9da <FSE_normalizeCount+0x14a>
	MOVL DX, 0x34(SP)                        // f9d3 movl   %edx,-0xc(%rsp)
	MOVL AX, R15                             // f9d7 movl   %eax,%r15d

Nf9da:
	MOVWLSX AX, CX                           // f9da movswl %ax,%ecx
	ADDL $1, DX                              // f9dd addl   $0x1,%edx
	MOVW AX, (R9)                            // f9e0 movw   %ax,(%r9)
	SUBL CX, R13                             // f9e4 subl   %ecx,%r13d
	CMPL R8, DX                              // f9e7 cmpl   %edx,%r8d
	JCC Nf972                                // f9ea jae    f972 <FSE_normalizeCount+0xe2>

Nf9ec:
	MOVL 0x34(SP), AX                        // f9ec movl   -0xc(%rsp),%eax
	MOVL R13, SI                             // f9f0 movl   %r13d,%esi
	NEGL SI                                  // f9f3 negl   %esi
	LEAQ (R10)(AX*2), CX                     // f9f5 leaq   (%r10,%rax,2),%rcx
	MOVWLZX (CX), DX                         // f9f9 movzwl (%rcx),%edx
	MOVL DX, AX                              // f9fc movl   %edx,%eax
	SARW $1, AX                              // f9fe sarw   $1,%ax
	CWDE                                     // fa01 cwtl
	CMPL SI, AX                              // fa02 cmpl   %eax,%esi
	JGE Nfa48                                // fa04 jge    fa48 <FSE_normalizeCount+0x1b8>
	ADDL R13, DX                             // fa06 addl   %r13d,%edx
	MOVW DX, (CX)                            // fa09 movw   %dx,(%rcx)

Nfa0c:
	MOVL 0x30(SP), AX                        // fa0c movl   -0x10(%rsp),%eax
	MOVQ AX, ret+48(FP)
	RET                                      // fa1a retq
	// fa1b nopl   0x0(%rax,%rax,1)

Nfa20:
	MOVQ $-1, AX                             // fa21 movq   $0xffffffffffffffff,%rax
	MOVQ AX, ret+48(FP)
	RET                                      // fa31 retq
	// fa32 nopw   0x0(%rax,%rax,1)

Nfa38:
	XORL AX, AX                              // fa39 xorl   %eax,%eax
	MOVQ AX, ret+48(FP)
	RET                                      // fa44 retq
	// fa45 nopl   (%rax)

Nfa48:
	MOVL 0x30(SP), AX                        // fa48 movl   -0x10(%rsp),%eax
	LEAQ (DI)(DI*2), R9                      // fa4c leaq   (%rdi,%rdi,2),%r9
	MOVL R8, SI                              // fa50 movl   %r8d,%esi
	MOVQ R9, BX                              // fa53 movq   %r9,%rbx
	LEAL 0x1(AX), CX                         // fa56 leal   0x1(%rax),%ecx
	XORL AX, AX                              // fa59 xorl   %eax,%eax
	SHRQ CX, BX                              // fa5b shrq   %cl,%rbx
	XORL CX, CX                              // fa5e xorl   %ecx,%ecx
	MOVL BX, R9                              // fa60 movl   %ebx,%r9d
	JMP Nfa84                                // fa63 jmp    fa84 <FSE_normalizeCount+0x1f4>
	// fa65 nopl   (%rax)

Nfa68:
	MOVL $-2, R13                            // fa68 movl   $0xfffffffe,%r13d
	CMPL R9, DX                              // fa6e cmpl   %edx,%r9d
	JCC Nfaa0                                // fa71 jae    faa0 <FSE_normalizeCount+0x210>

Nfa73:
	MOVW R13, (R10)(AX*2)                    // fa73 movw   %r13w,(%r10,%rax,2)
	LEAQ 0x1(AX), DX                         // fa78 leaq   0x1(%rax),%rdx
	CMPQ AX, SI                              // fa7c cmpq   %rsi,%rax
	JEQ Nfab0                                // fa7f je     fab0 <FSE_normalizeCount+0x220>
	MOVQ DX, AX                              // fa81 movq   %rdx,%rax

Nfa84:
	MOVL (R11)(AX*4), DX                     // fa84 movl   (%r11,%rax,4),%edx
	XORL R13, R13                            // fa88 xorl   %r13d,%r13d
	TESTL DX, DX                             // fa8b testl  %edx,%edx
	JEQ Nfa73                                // fa8d je     fa73 <FSE_normalizeCount+0x1e3>
	CMPL R14, DX                             // fa8f cmpl   %edx,%r14d
	JCS Nfa68                                // fa92 jb     fa68 <FSE_normalizeCount+0x1d8>
	ADDL $1, CX                              // fa94 addl   $0x1,%ecx
	SUBQ DX, DI                              // fa97 subq   %rdx,%rdi
	MOVL BP, R13                             // fa9a movl   %ebp,%r13d
	JMP Nfa73                                // fa9d jmp    fa73 <FSE_normalizeCount+0x1e3>
	// fa9f nop

Nfaa0:
	ADDL $1, CX                              // faa0 addl   $0x1,%ecx
	SUBQ DX, DI                              // faa3 subq   %rdx,%rdi
	MOVL $1, R13                             // faa6 movl   $0x1,%r13d
	JMP Nfa73                                // faac jmp    fa73 <FSE_normalizeCount+0x1e3>
	// faae xchgw  %ax,%ax

Nfab0:
	MOVL 0x3c(SP), R13                       // fab0 movl   -0x4(%rsp),%r13d
	SUBL CX, R13                             // fab5 subl   %ecx,%r13d
	JEQ Nfa0c                                // fab8 je     fa0c <FSE_normalizeCount+0x17c>
	MOVL R13, BP                             // fabe movl   %r13d,%ebp
	MOVQ DI, AX                              // fac1 movq   %rdi,%rax
	XORL DX, DX                              // fac4 xorl   %edx,%edx
	MOVL BX, R9                              // fac6 movl   %ebx,%r9d
	DIVQ BP                                  // fac9 divq   %rbp
	CMPQ R9, AX                              // facc cmpq   %rax,%r9
	JCS Nfb5f                                // facf jb     fb5f <FSE_normalizeCount+0x2cf>
	ADDL $1, R8                              // fad5 addl   $0x1,%r8d
	CMPL CX, R8                              // fad9 cmpl   %r8d,%ecx
	JEQ Nfc0a                                // fadc je     fc0a <FSE_normalizeCount+0x37a>
	TESTQ DI, DI                             // fae2 testq  %rdi,%rdi
	JEQ Nfbcd                                // fae5 je     fbcd <FSE_normalizeCount+0x33d>

Nfaeb:
	MOVL 0x30(SP), AX                        // faeb movl   -0x10(%rsp),%eax
	MOVL $61, CX                             // faef movl   $0x3d,%ecx
	MOVL DI, DI                              // faf4 movl   %edi,%edi
	XORL DX, DX                              // faf6 xorl   %edx,%edx
	MOVL $1, R8                              // faf8 movl   $0x1,%r8d
	SUBL AX, CX                              // fafe subl   %eax,%ecx
	SHLQ CX, R8                              // fb00 shlq   %cl,%r8
	MOVL R12, CX                             // fb03 movl   %r12d,%ecx
	SHLQ CX, BP                              // fb06 shlq   %cl,%rbp
	SUBQ $1, R8                              // fb09 subq   $0x1,%r8
	MOVL R12, CX                             // fb0d movl   %r12d,%ecx
	LEAQ (BP)(R8*1), AX                      // fb10 leaq   0x0(%rbp,%r8,1),%rax
	DIVQ DI                                  // fb15 divq   %rdi
	XORL DX, DX                              // fb18 xorl   %edx,%edx
	JMP Nfb30                                // fb1a jmp    fb30 <FSE_normalizeCount+0x2a0>
	// fb1c nopl   0x0(%rax)

Nfb20:
	LEAQ 0x1(DX), DI                         // fb20 leaq   0x1(%rdx),%rdi
	CMPQ DX, SI                              // fb24 cmpq   %rsi,%rdx
	JEQ Nfa0c                                // fb27 je     fa0c <FSE_normalizeCount+0x17c>
	MOVQ DI, DX                              // fb2d movq   %rdi,%rdx

Nfb30:
	CMPW (R10)(DX*2), $-2                    // fb30 cmpw   $0xfffe,(%r10,%rdx,2)
	JNE Nfb20                                // fb36 jne    fb20 <FSE_normalizeCount+0x290>
	MOVL (R11)(DX*4), DI                     // fb38 movl   (%r11,%rdx,4),%edi
	IMULQ AX, DI                             // fb3c imulq  %rax,%rdi
	ADDQ R8, DI                              // fb40 addq   %r8,%rdi
	SHRQ CX, R8                              // fb43 shrq   %cl,%r8
	MOVQ DI, R9                              // fb46 movq   %rdi,%r9
	SHRQ CX, R9                              // fb49 shrq   %cl,%r9
	SUBL R8, R9                              // fb4c subl   %r8d,%r9d
	JEQ Nfa20                                // fb4f je     fa20 <FSE_normalizeCount+0x190>
	MOVW R9, (R10)(DX*2)                     // fb55 movw   %r9w,(%r10,%rdx,2)
	MOVQ DI, R8                              // fb5a movq   %rdi,%r8
	JMP Nfb20                                // fb5d jmp    fb20 <FSE_normalizeCount+0x290>

Nfb5f:
	LEAQ (DI)(DI*2), AX                      // fb5f leaq   (%rdi,%rdi,2),%rax
	LEAL (R13)(R13*1), R9                    // fb63 leal   0x0(%r13,%r13,1),%r9d
	XORL DX, DX                              // fb68 xorl   %edx,%edx
	DIVQ R9                                  // fb6a divq   %r9
	XORL DX, DX                              // fb6d xorl   %edx,%edx
	JMP Nfb84                                // fb6f jmp    fb84 <FSE_normalizeCount+0x2f4>
	// fb71 nopl   0x0(%rax)

Nfb78:
	LEAQ 0x1(DX), R9                         // fb78 leaq   0x1(%rdx),%r9
	CMPQ DX, SI                              // fb7c cmpq   %rsi,%rdx
	JEQ Nfba7                                // fb7f je     fba7 <FSE_normalizeCount+0x317>
	MOVQ R9, DX                              // fb81 movq   %r9,%rdx

Nfb84:
	CMPW (R10)(DX*2), $-2                    // fb84 cmpw   $0xfffe,(%r10,%rdx,2)
	JNE Nfb78                                // fb8a jne    fb78 <FSE_normalizeCount+0x2e8>
	MOVL (R11)(DX*4), R9                     // fb8c movl   (%r11,%rdx,4),%r9d
	CMPL AX, R9                              // fb90 cmpl   %r9d,%eax
	JCS Nfb78                                // fb93 jb     fb78 <FSE_normalizeCount+0x2e8>
	MOVL $1, BX                              // fb95 movl   $0x1,%ebx
	ADDL $1, CX                              // fb9a addl   $0x1,%ecx
	SUBQ R9, DI                              // fb9d subq   %r9,%rdi
	MOVW BX, (R10)(DX*2)                     // fba0 movw   %bx,(%r10,%rdx,2)
	JMP Nfb78                                // fba5 jmp    fb78 <FSE_normalizeCount+0x2e8>

Nfba7:
	MOVL 0x3c(SP), R13                       // fba7 movl   -0x4(%rsp),%r13d
	ADDL $1, R8                              // fbac addl   $0x1,%r8d
	SUBL CX, R13                             // fbb0 subl   %ecx,%r13d
	CMPL R8, CX                              // fbb3 cmpl   %ecx,%r8d
	JEQ Nfc0a                                // fbb6 je     fc0a <FSE_normalizeCount+0x37a>
	MOVL R13, BP                             // fbb8 movl   %r13d,%ebp
	TESTQ DI, DI                             // fbbb testq  %rdi,%rdi
	JNE Nfaeb                                // fbbe jne    faeb <FSE_normalizeCount+0x25b>
	TESTL R13, R13                           // fbc4 testl  %r13d,%r13d
	JEQ Nfa0c                                // fbc7 je     fa0c <FSE_normalizeCount+0x17c>

Nfbcd:
	XORL DX, DX                              // fbcd xorl   %edx,%edx
	// fbcf nop

Nfbd0:
	MOVL DX, AX                              // fbd0 movl   %edx,%eax
	LEAQ (R10)(AX*2), CX                     // fbd2 leaq   (%r10,%rax,2),%rcx
	MOVWLZX (CX), AX                         // fbd6 movzwl (%rcx),%eax
	TESTW AX, AX                             // fbd9 testw  %ax,%ax
	JLE Nfc00                                // fbdc jle    fc00 <FSE_normalizeCount+0x370>
	ADDL $1, AX                              // fbde addl   $0x1,%eax
	MOVW AX, (CX)                            // fbe1 movw   %ax,(%rcx)
	LEAL 0x1(DX), AX                         // fbe4 leal   0x1(%rdx),%eax
	XORL DX, DX                              // fbe7 xorl   %edx,%edx
	DIVL R8                                  // fbe9 divl   %r8d
	SUBL $1, R13                             // fbec subl   $0x1,%r13d
	JNE Nfbd0                                // fbf0 jne    fbd0 <FSE_normalizeCount+0x340>
	JMP Nfa0c                                // fbf2 jmpq   fa0c <FSE_normalizeCount+0x17c>
	// fbf7 nopw   0x0(%rax,%rax,1)

Nfc00:
	LEAL 0x1(DX), AX                         // fc00 leal   0x1(%rdx),%eax
	XORL DX, DX                              // fc03 xorl   %edx,%edx
	DIVL R8                                  // fc05 divl   %r8d
	JMP Nfbd0                                // fc08 jmp    fbd0 <FSE_normalizeCount+0x340>

Nfc0a:
	XORL AX, AX                              // fc0a xorl   %eax,%eax
	XORL DI, DI                              // fc0c xorl   %edi,%edi
	XORL DX, DX                              // fc0e xorl   %edx,%edx
	JMP Nfc1b                                // fc10 jmp    fc1b <FSE_normalizeCount+0x38b>
	// fc12 nopw   0x0(%rax,%rax,1)

Nfc18:
	MOVQ CX, AX                              // fc18 movq   %rcx,%rax

Nfc1b:
	MOVL (R11)(AX*4), CX                     // fc1b movl   (%r11,%rax,4),%ecx
	CMPL DX, CX                              // fc1f cmpl   %ecx,%edx
	CMOVLCS CX, DX                           // fc21 cmovbl %ecx,%edx
	CMOVLCS AX, DI                           // fc24 cmovbl %eax,%edi
	LEAQ 0x1(AX), CX                         // fc27 leaq   0x1(%rax),%rcx
	CMPQ AX, SI                              // fc2b cmpq   %rsi,%rax
	JNE Nfc18                                // fc2e jne    fc18 <FSE_normalizeCount+0x388>
	MOVL DI, DI                              // fc30 movl   %edi,%edi
	ADDW R13, (R10)(DI*2)                    // fc32 addw   %r13w,(%r10,%rdi,2)
	JMP Nfa0c                                // fc37 jmpq   fa0c <FSE_normalizeCount+0x17c>
	// fc3c nopl   0x0(%rax)

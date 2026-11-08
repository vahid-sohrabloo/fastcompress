//go:build amd64 && !appengine && !noasm && !purego && gc

// buildCTableAsm is gcc's FSE_buildCTable_wksp from ref/zstd/programs/zstd
// (zstd v1.5.7, 0xf390-0xf750), transliterated instruction for instruction
// from C. Each line keeps C's address and instruction in its comment.
// Registers are C's (rax = AX, ..., r15 = R15); C's red-zone slots at
// -0x34..-0x10(%rsp) are at 0x0c..0x30(SP) (C's offset + 0x40).
//
// Departures from gcc's code:
//   - no endbr64 or register pushes (the registers are scratch in ABI0);
//   - the arguments come from the Go frame;
//   - R14 sums the norm while cumul is built (-1 counting 1), and a sum
//     other than tableSize returns -1 before anything is written past the
//     header, cumul and the low-probability slots of tableSymbol. C keeps
//     tableSize+1 in R14 there and has no check: an invalid norm makes it
//     write out of bounds. A norm below -1 adds at least 0x8000 (it is read
//     zero-extended), so it fails the check too.
//
// Go arguments (FP offsets, checked by go vet's asmdecl): ct+0, norm+8,
// maxSymbol+16, tableLog+24, wksp+32, wkspSize+40, ret+48.

#include "textflag.h"

TEXT ·buildCTableAsm(SB), NOSPLIT, $64-56
	MOVQ ct+0(FP), DI
	MOVQ norm+8(FP), SI
	MOVQ maxSymbol+16(FP), DX
	MOVQ tableLog+24(FP), CX
	MOVQ wksp+32(FP), R8
	MOVQ wkspSize+40(FP), R9
	MOVL $1, AX                              // f396 movl   $0x1,%eax
	MOVL DX, R15                             // f39b movl   %edx,%r15d
	MOVQ SI, R11                             // f39e movq   %rsi,%r11
	SHLL CX, AX                              // f3a3 shll   %cl,%eax
	LEAL -0x1(AX), DX                        // f3a7 leal   -0x1(%rax),%edx
	MOVL $8, R13                             // f3aa movl   $0x8,%r13d
	MOVQ DI, BX                              // f3b4 movq   %rdi,%rbx
	MOVL AX, 0xc(SP)                         // f3b7 movl   %eax,-0x34(%rsp)
	SHRL $1, AX                              // f3bb shrl   $1,%eax
	MOVL CX, 0x18(SP)                        // f3bd movl   %ecx,-0x28(%rsp)
	TESTL CX, CX                             // f3c1 testl  %ecx,%ecx
	JEQ Ff3d0                                // f3c3 je     f3d0 <FSE_buildCTable_wksp+0x40>
	LEAL 0x1(AX), CX                         // f3c5 leal   0x1(%rax),%ecx
	LEAQ (CX*4), R13                         // f3c8 leaq   0x0(,%rcx,4),%r13

Ff3d0:
	MOVL 0xc(SP), R14                        // f3d0 movl   -0x34(%rsp),%r14d
	MOVL 0x18(SP), BP                        // f3d5 movl   -0x28(%rsp),%ebp
	LEAL 0x2(R15), R10                       // f3d9 leal   0x2(%r15),%r10d
	LEAL 0x1(R15), R12                       // f3dd leal   0x1(%r15),%r12d
	MOVL $1, DI                              // f3e1 movl   $0x1,%edi
	LEAQ (R10)(R10*1), SI                    // f3e6 leaq   (%r10,%r10,1),%rsi
	MOVL R14, CX                             // f3ea movl   %r14d,%ecx
	MOVQ SI, 0x10(SP)                        // f3ed movq   %rsi,-0x30(%rsp)
	ADDQ R8, SI                              // f3f2 addq   %r8,%rsi
	SHRL $3, CX                              // f3f5 shrl   $0x3,%ecx
	LEAL 0x3(AX)(CX*1), AX                   // f3f8 leal   0x3(%rax,%rcx,1),%eax
	MOVL BP, CX                              // f3fc movl   %ebp,%ecx
	SHLQ CX, DI                              // f3fe shlq   %cl,%rdi
	LEAQ (DI)(R10*1), CX                     // f401 leaq   (%rdi,%r10,1),%rcx
	SHRQ $1, CX                              // f405 shrq   $1,%rcx
	LEAQ 0x8(CX*4), DI                       // f408 leaq   0x8(,%rcx,4),%rdi
	MOVQ $-44, CX                            // f410 movq   $0xffffffffffffffd4,%rcx
	CMPQ R9, DI                              // f417 cmpq   %rdi,%r9
	JCS Ff515                                // f41a jb     f515 <FSE_buildCTable_wksp+0x185>
	XORL CX, CX                              // f420 xorl   %ecx,%ecx
	MOVL R14, DI                             // f422 movl   %r14d,%edi
	MOVW BP, (BX)                            // f425 movw   %bp,(%rbx)
	MOVW R15, 0x2(BX)                        // f428 movw   %r15w,0x2(%rbx)
	MOVW R14, 0x1e(SP)                       // f42d movw   %r14w,-0x22(%rsp)
	XORL R14, R14                            // f433 leal 0x1(%rdi),%r14d: R14 sums the norm instead
	MOVW CX, (R8)                            // f437 movw   %cx,(%r8)
	TESTL R12, R12                           // f43b testl  %r12d,%r12d
	JEQ Ff73a                                // f43e je     f73a <FSE_buildCTable_wksp+0x3aa>
	MOVQ R13, 0x20(SP)                       // f444 movq   %r13,-0x20(%rsp)
	MOVL DX, DI                              // f449 movl   %edx,%edi
	XORL BP, BP                              // f44b xorl   %ebp,%ebp
	XORL R9, R9                              // f44d xorl   %r9d,%r9d
	MOVL $1, R10                             // f450 movl   $0x1,%r10d
	XORL CX, CX                              // f456 xorl   %ecx,%ecx
	JMP Ff47b                                // f458 jmp    f47b <FSE_buildCTable_wksp+0xeb>
	// f45a nopw   0x0(%rax,%rax,1)

Ff460:
	ADDL CX, R14                             // (not in C) sum the norm
	ADDL R9, CX                              // f460 addl   %r9d,%ecx
	ADDL $1, R10                             // f463 addl   $0x1,%r10d
	ADDL $1, BP                              // f467 addl   $0x1,%ebp
	MOVW CX, (R13)                           // f46a movw   %cx,0x0(%r13)
	CMPL R12, R10                            // f46f cmpl   %r10d,%r12d
	JCS Ff4ab                                // f472 jb     f4ab <FSE_buildCTable_wksp+0x11b>

Ff474:
	MOVL BP, CX                              // f474 movl   %ebp,%ecx
	MOVWLZX (R8)(CX*2), R9                   // f476 movzwl (%r8,%rcx,2),%r9d

Ff47b:
	MOVWLZX (R11)(CX*2), CX                  // f47b movzwl (%r11,%rcx,2),%ecx
	MOVL R10, R13                            // f480 movl   %r10d,%r13d
	LEAQ (R8)(R13*2), R13                    // f483 leaq   (%r8,%r13,2),%r13
	CMPW CX, $-1                             // f487 cmpw   $0xffff,%cx
	JNE Ff460                                // f48b jne    f460 <FSE_buildCTable_wksp+0xd0>
	ADDL $1, R14                             // (not in C) a -1 counts 1
	MOVL DI, CX                              // f48d movl   %edi,%ecx
	ADDL $1, R9                              // f48f addl   $0x1,%r9d
	ADDL $1, R10                             // f493 addl   $0x1,%r10d
	SUBL $1, DI                              // f497 subl   $0x1,%edi
	MOVW R9, (R13)                           // f49a movw   %r9w,0x0(%r13)
	MOVB BPB, (SI)(CX*1)                     // f49f movb   %bpl,(%rsi,%rcx,1)
	ADDL $1, BP                              // f4a3 addl   $0x1,%ebp
	CMPL R12, R10                            // f4a6 cmpl   %r10d,%r12d
	JCC Ff474                                // f4a9 jae    f474 <FSE_buildCTable_wksp+0xe4>

Ff4ab:
	CMPL R14, 0xc(SP)                        // (not in C) the norm must sum to tableSize
	JNE badnorm
	ADDL $1, R14                             // tableSize+1, C's R14
	MOVW R14, (R8)(R12*2)                    // f4ab movw   %r14w,(%r8,%r12,2)
	MOVQ 0x20(SP), R13                       // f4b0 movq   -0x20(%rsp),%r13
	MOVL R15, R12                            // f4b5 movl   %r15d,%r12d
	XORL R14, R14                            // f4b8 xorl   %r14d,%r14d
	XORL CX, CX                              // f4bb xorl   %ecx,%ecx
	CMPL DI, DX                              // f4bd cmpl   %edx,%edi
	JEQ Ff523                                // f4bf je     f523 <FSE_buildCTable_wksp+0x193>
	MOVQ R13, 0x10(SP)                       // f4c1 movq   %r13,-0x30(%rsp)
	// f4c6 cs nopw 0x0(%rax,%rax,1)

Ff4d0:
	MOVWLSX (R11)(R14*2), BP                 // f4d0 movswl (%r11,%r14,2),%ebp
	TESTL BP, BP                             // f4d5 testl  %ebp,%ebp
	JLE Ff501                                // f4d7 jle    f501 <FSE_buildCTable_wksp+0x171>
	MOVL R14, R13                            // f4d9 movl   %r14d,%r13d
	XORL R9, R9                              // f4dc xorl   %r9d,%r9d
	// f4df nop

Ff4e0:
	MOVL CX, R10                             // f4e0 movl   %ecx,%r10d
	MOVB R13B, (SI)(R10*1)                   // f4e3 movb   %r13b,(%rsi,%r10,1)
	// f4e7 nopw   0x0(%rax,%rax,1)

Ff4f0:
	ADDL AX, CX                              // f4f0 addl   %eax,%ecx
	ANDL DX, CX                              // f4f2 andl   %edx,%ecx
	CMPL DI, CX                              // f4f4 cmpl   %ecx,%edi
	JCS Ff4f0                                // f4f6 jb     f4f0 <FSE_buildCTable_wksp+0x160>
	ADDL $1, R9                              // f4f8 addl   $0x1,%r9d
	CMPL BP, R9                              // f4fc cmpl   %r9d,%ebp
	JNE Ff4e0                                // f4ff jne    f4e0 <FSE_buildCTable_wksp+0x150>

Ff501:
	LEAQ 0x1(R14), R9                        // f501 leaq   0x1(%r14),%r9
	CMPQ R12, R14                            // f505 cmpq   %r14,%r12
	JEQ Ff726                                // f508 je     f726 <FSE_buildCTable_wksp+0x396>
	MOVQ R9, R14                             // f50e movq   %r9,%r14
	JMP Ff4d0                                // f511 jmp    f4d0 <FSE_buildCTable_wksp+0x140>

Ff513:
	XORL CX, CX                              // f513 xorl   %ecx,%ecx

Ff515:
	MOVQ CX, ret+48(FP)                      // f516 movq   %rcx,%rax
	RET                                      // f522 retq

Ff523:
	MOVL 0xc(SP), CX                         // f523 movl   -0x34(%rsp),%ecx
	LEAQ 0x2(R11)(R12*2), BP                 // f527 leaq   0x2(%r11,%r12,2),%rbp
	MOVL DX, 0x28(SP)                        // f52c movl   %edx,-0x18(%rsp)
	MOVQ R11, R14                            // f530 movq   %r11,%r14
	MOVQ 0x10(SP), R12                       // f533 movq   -0x30(%rsp),%r12
	MOVL AX, 0x2c(SP)                        // f538 movl   %eax,-0x14(%rsp)
	XORL R10, R10                            // f53c xorl   %r10d,%r10d
	XORL R9, R9                              // f53f xorl   %r9d,%r9d
	MOVQ CX, 0x10(SP)                        // f542 movq   %rcx,-0x30(%rsp)
	LEAQ (SI)(CX*1), DI                      // f547 leaq   (%rsi,%rcx,1),%rdi
	MOVQ BP, AX                              // f54b movq   %rbp,%rax
	LEAQ 0x8(R12)(CX*1), R12                 // f54e leaq   0x8(%r12,%rcx,1),%r12
	MOVQ SI, 0x30(SP)                        // f553 movq   %rsi,-0x10(%rsp)
	MOVQ R12, SI                             // f558 movq   %r12,%rsi

Ff55b:
	MOVWQSX (R14), CX                        // f55b movswq (%r14),%rcx
	MOVQ R10, (DI)(R9*1)                     // f55f movq   %r10,(%rdi,%r9,1)
	CMPW CX, $8                              // f563 cmpw   $0x8,%cx
	JLE Ff5dc                                // f567 jle    f5dc <FSE_buildCTable_wksp+0x24c>
	MOVWLSX CX, DX                           // f569 movswl %cx,%edx
	SUBL $9, DX                              // f56c subl   $0x9,%edx
	MOVL DX, BP                              // f56f movl   %edx,%ebp
	SHRL $3, BP                              // f571 shrl   $0x3,%ebp
	ADDL $1, BP                              // f574 addl   $0x1,%ebp
	CMPL DX, $7                              // f577 cmpl   $0x7,%edx
	JLS Ff730                                // f57a jbe    f730 <FSE_buildCTable_wksp+0x3a0>
	MOVL BP, R12                             // f580 movl   %ebp,%r12d
	LEAQ (SI)(R9*1), DX                      // f583 leaq   (%rsi,%r9,1),%rdx
	MOVQ R10, X0                             // f587 movq   %r10,%xmm0
	SHRL $1, R12                             // f58c shrl   $1,%r12d
	ADDQ R8, DX                              // f58f addq   %r8,%rdx
	PUNPCKLQDQ X0, X0                        // f592 punpcklqdq %xmm0,%xmm0
	SHLQ $4, R12                             // f596 shlq   $0x4,%r12
	LEAQ (R12)(DX*1), R13                    // f59a leaq   (%r12,%rdx,1),%r13
	ANDL $16, R12                            // f59e andl   $0x10,%r12d
	JEQ Ff5b0                                // f5a2 je     f5b0 <FSE_buildCTable_wksp+0x220>
	MOVUPS X0, (DX)                          // f5a4 movups %xmm0,(%rdx)
	ADDQ $16, DX                             // f5a7 addq   $0x10,%rdx
	CMPQ DX, R13                             // f5ab cmpq   %r13,%rdx
	JEQ Ff5c0                                // f5ae je     f5c0 <FSE_buildCTable_wksp+0x230>

Ff5b0:
	MOVUPS X0, (DX)                          // f5b0 movups %xmm0,(%rdx)
	ADDQ $32, DX                             // f5b3 addq   $0x20,%rdx
	MOVUPS X0, -0x10(DX)                     // f5b7 movups %xmm0,-0x10(%rdx)
	CMPQ DX, R13                             // f5bb cmpq   %r13,%rdx
	JNE Ff5b0                                // f5be jne    f5b0 <FSE_buildCTable_wksp+0x220>

Ff5c0:
	TESTB $1, BPB                            // f5c0 testb  $0x1,%bpl
	JEQ Ff5dc                                // f5c4 je     f5dc <FSE_buildCTable_wksp+0x24c>
	ANDL $-2, BP                             // f5c6 andl   $0xfffffffe,%ebp
	LEAL 0x8(BP*8), DX                       // f5c9 leal   0x8(,%rbp,8),%edx

Ff5d0:
	MOVLQSX DX, DX                           // f5d0 movslq %edx,%rdx
	LEAQ (DI)(R9*1), BP                      // f5d3 leaq   (%rdi,%r9,1),%rbp
	MOVQ R10, (BP)(DX*1)                     // f5d7 movq   %r10,0x0(%rbp,%rdx,1)

Ff5dc:
	MOVQ $0x101010101010101, DX              // f5dc movabsq $0x101010101010101,%rdx
	ADDQ $2, R14                             // f5e6 addq   $0x2,%r14
	ADDQ CX, R9                              // f5ea addq   %rcx,%r9
	ADDQ DX, R10                             // f5ed addq   %rdx,%r10
	CMPQ AX, R14                             // f5f0 cmpq   %r14,%rax
	JNE Ff55b                                // f5f3 jne    f55b <FSE_buildCTable_wksp+0x1cb>
	MOVQ 0x10(SP), CX                        // f5f9 movq   -0x30(%rsp),%rcx
	MOVQ 0x20(SP), R13                       // f5fe movq   -0x20(%rsp),%r13
	MOVL 0x28(SP), DX                        // f603 movl   -0x18(%rsp),%edx
	MOVL 0x2c(SP), AX                        // f607 movl   -0x14(%rsp),%eax
	MOVQ 0x30(SP), SI                        // f60b movq   -0x10(%rsp),%rsi

Ff610:
	TESTQ CX, CX                             // f610 testq  %rcx,%rcx
	JEQ Ff692                                // f613 je     f692 <FSE_buildCTable_wksp+0x302>
	SUBQ $1, CX                              // f615 subq   $0x1,%rcx
	MOVL DX, R9                              // f619 movl   %edx,%r9d
	ANDQ $-2, CX                             // f61c andq   $0xfffffffffffffffe,%rcx
	LEAQ 0x2(DI)(CX*1), R10                  // f620 leaq   0x2(%rdi,%rcx,1),%r10
	XORL CX, CX                              // f625 xorl   %ecx,%ecx

Ff627:
	MOVBLZX (DI), R12                        // f627 movzbl (%rdi),%r12d
	MOVQ CX, BP                              // f62b movq   %rcx,%rbp
	ADDQ AX, CX                              // f62e addq   %rax,%rcx
	ADDQ $2, DI                              // f631 addq   $0x2,%rdi
	ANDQ R9, BP                              // f635 andq   %r9,%rbp
	MOVB R12B, (SI)(BP*1)                    // f638 movb   %r12b,(%rsi,%rbp,1)
	MOVQ CX, BP                              // f63c movq   %rcx,%rbp
	MOVBLZX -0x1(DI), R12                    // f63f movzbl -0x1(%rdi),%r12d
	ADDQ AX, CX                              // f644 addq   %rax,%rcx
	ANDQ R9, BP                              // f647 andq   %r9,%rbp
	ANDQ R9, CX                              // f64a andq   %r9,%rcx
	MOVB R12B, (SI)(BP*1)                    // f64d movb   %r12b,(%rsi,%rbp,1)
	CMPQ R10, DI                             // f651 cmpq   %rdi,%r10
	JNE Ff627                                // f654 jne    f627 <FSE_buildCTable_wksp+0x297>

Ff656:
	MOVL 0xc(SP), AX                         // f656 movl   -0x34(%rsp),%eax
	TESTL AX, AX                             // f65a testl  %eax,%eax
	JEQ Ff692                                // f65c je     f692 <FSE_buildCTable_wksp+0x302>
	MOVWLZX 0x1e(SP), R9                     // f65e movzwl -0x22(%rsp),%r9d
	LEAQ 0x1(SI)(DX*1), CX                   // f664 leaq   0x1(%rsi,%rdx,1),%rcx
	SUBL SI, R9                              // f669 subl   %esi,%r9d
	// f66c nopl   0x0(%rax)

Ff670:
	MOVBLZX (SI), AX                         // f670 movzbl (%rsi),%eax
	LEAQ (R8)(AX*2), DX                      // f673 leaq   (%r8,%rax,2),%rdx
	MOVWLZX (DX), AX                         // f677 movzwl (%rdx),%eax
	LEAL 0x1(AX), DI                         // f67a leal   0x1(%rax),%edi
	MOVW DI, (DX)                            // f67d movw   %di,(%rdx)
	LEAL (R9)(SI*1), DX                      // f680 leal   (%r9,%rsi,1),%edx
	ADDQ $1, SI                              // f684 addq   $0x1,%rsi
	MOVW DX, 0x4(BX)(AX*2)                   // f688 movw   %dx,0x4(%rbx,%rax,2)
	CMPQ SI, CX                              // f68d cmpq   %rcx,%rsi
	JNE Ff670                                // f690 jne    f670 <FSE_buildCTable_wksp+0x2e0>

Ff692:
	MOVL 0x18(SP), AX                        // f692 movl   -0x28(%rsp),%eax
	MOVL 0xc(SP), SI                         // f696 movl   -0x34(%rsp),%esi
	XORL R9, R9                              // f69a xorl   %r9d,%r9d
	LEAL 0x1(AX), DI                         // f69d leal   0x1(%rax),%edi
	MOVL AX, R8                              // f6a0 movl   %eax,%r8d
	LEAL -0x1f(AX), R10                      // f6a3 leal   -0x1f(%rax),%r10d
	SHLL $16, DI                             // f6a7 shll   $0x10,%edi
	SHLL $16, R8                             // f6aa shll   $0x10,%r8d
	SUBL SI, DI                              // f6ae subl   %esi,%edi
	SUBL SI, R8                              // f6b0 subl   %esi,%r8d
	XORL SI, SI                              // f6b3 xorl   %esi,%esi
	JMP Ff6fd                                // f6b5 jmp    f6fd <FSE_buildCTable_wksp+0x36d>
	// f6b7 nopw   0x0(%rax,%rax,1)

Ff6c0:
	LEAL 0x1(DX), CX                         // f6c0 leal   0x1(%rdx),%ecx
	TESTW $-3, CX                            // f6c3 testw  $0xfffd,%cx
	JEQ Ff716                                // f6c8 je     f716 <FSE_buildCTable_wksp+0x386>
	LEAL -0x1(DX), CX                        // f6ca leal   -0x1(%rdx),%ecx
	MOVL DX, R14                             // f6cd movl   %edx,%r14d
	BSRL CX, CX                              // f6d0 bsrl   %ecx,%ecx
	XORL $31, CX                             // f6d3 xorl   $0x1f,%ecx
	ADDL R10, CX                             // f6d6 addl   %r10d,%ecx
	MOVL CX, BP                              // f6d9 movl   %ecx,%ebp
	SHLL CX, R14                             // f6db shll   %cl,%r14d
	MOVL R9, CX                              // f6de movl   %r9d,%ecx
	ADDL DX, R9                              // f6e1 addl   %edx,%r9d
	SHLL $16, BP                             // f6e4 shll   $0x10,%ebp
	SUBL DX, CX                              // f6e7 subl   %edx,%ecx
	SUBL R14, BP                             // f6e9 subl   %r14d,%ebp
	MOVL CX, (AX)                            // f6ec movl   %ecx,(%rax)
	MOVL BP, 0x4(AX)                         // f6ee movl   %ebp,0x4(%rax)

Ff6f1:
	ADDL $1, SI                              // f6f1 addl   $0x1,%esi
	CMPL R15, SI                             // f6f4 cmpl   %esi,%r15d
	JCS Ff513                                // f6f7 jb     f513 <FSE_buildCTable_wksp+0x183>

Ff6fd:
	MOVL SI, AX                              // f6fd movl   %esi,%eax
	MOVWLSX (R11)(AX*2), DX                  // f6ff movswl (%r11,%rax,2),%edx
	LEAQ (R13)(AX*8), AX                     // f704 leaq   0x0(%r13,%rax,8),%rax
	ADDQ BX, AX                              // f709 addq   %rbx,%rax
	TESTW DX, DX                             // f70c testw  %dx,%dx
	JNE Ff6c0                                // f70f jne    f6c0 <FSE_buildCTable_wksp+0x330>
	MOVL DI, 0x4(AX)                         // f711 movl   %edi,0x4(%rax)
	JMP Ff6f1                                // f714 jmp    f6f1 <FSE_buildCTable_wksp+0x361>

Ff716:
	LEAL -0x1(R9), DX                        // f716 leal   -0x1(%r9),%edx
	MOVL R8, 0x4(AX)                         // f71a movl   %r8d,0x4(%rax)
	ADDL $1, R9                              // f71e addl   $0x1,%r9d
	MOVL DX, (AX)                            // f722 movl   %edx,(%rax)
	JMP Ff6f1                                // f724 jmp    f6f1 <FSE_buildCTable_wksp+0x361>

Ff726:
	MOVQ 0x10(SP), R13                       // f726 movq   -0x30(%rsp),%r13
	JMP Ff656                                // f72b jmpq   f656 <FSE_buildCTable_wksp+0x2c6>

Ff730:
	MOVL $8, DX                              // f730 movl   $0x8,%edx
	JMP Ff5d0                                // f735 jmpq   f5d0 <FSE_buildCTable_wksp+0x240>

Ff73a:
	MOVL 0xc(SP), CX                         // f73a movl   -0x34(%rsp),%ecx
	LEAL 1(CX), R14                          // (not in C) tableSize+1, C's R14
	MOVW R14, (R8)                           // f73e movw   %r14w,(%r8)
	LEAQ (SI)(CX*1), DI                      // f742 leaq   (%rsi,%rcx,1),%rdi
	JMP Ff610                                // f746 jmpq   f610 <FSE_buildCTable_wksp+0x280>
	// f74b nopl   0x0(%rax,%rax,1)

badnorm:
	MOVQ $-1, ret+48(FP)
	RET

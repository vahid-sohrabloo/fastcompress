//go:build amd64 && !appengine && !noasm && !purego && gc

// compressUsingCTableAsm is gcc's FSE_compress_usingCTable_generic from
// ref/zstd/programs/zstd (zstd v1.5.7, 0xed50-0xf390), both the fast and the
// checked flush inlined as gcc inlined them, transliterated instruction for
// instruction from C. Each line keeps C's address and instruction in
// its comment. Registers are C's (rax = AX, ..., r15 = R15); C's red-zone
// slots at -0x30..-0x8(%rsp) are at 0x10..0x38(SP) (C's offset + 0x40).
//
// Departures from gcc's code: no register pushes (the registers are scratch
// in ABI0), the arguments come from the Go frame, and each retq stores RAX
// to ret.
//
// Go arguments (FP offsets, checked by go vet's asmdecl): dst+0, dstSize+8,
// src+16, srcSize+24, ct+32, fast+40, ret+48.

#include "textflag.h"

// bitstream.h's BIT_mask.
DATA fseBitMask<>+0(SB)/4, $0
DATA fseBitMask<>+4(SB)/4, $1
DATA fseBitMask<>+8(SB)/4, $3
DATA fseBitMask<>+12(SB)/4, $7
DATA fseBitMask<>+16(SB)/4, $15
DATA fseBitMask<>+20(SB)/4, $31
DATA fseBitMask<>+24(SB)/4, $63
DATA fseBitMask<>+28(SB)/4, $127
DATA fseBitMask<>+32(SB)/4, $255
DATA fseBitMask<>+36(SB)/4, $511
DATA fseBitMask<>+40(SB)/4, $1023
DATA fseBitMask<>+44(SB)/4, $2047
DATA fseBitMask<>+48(SB)/4, $4095
DATA fseBitMask<>+52(SB)/4, $8191
DATA fseBitMask<>+56(SB)/4, $16383
DATA fseBitMask<>+60(SB)/4, $32767
DATA fseBitMask<>+64(SB)/4, $65535
DATA fseBitMask<>+68(SB)/4, $131071
DATA fseBitMask<>+72(SB)/4, $262143
DATA fseBitMask<>+76(SB)/4, $524287
DATA fseBitMask<>+80(SB)/4, $1048575
DATA fseBitMask<>+84(SB)/4, $2097151
DATA fseBitMask<>+88(SB)/4, $4194303
DATA fseBitMask<>+92(SB)/4, $8388607
DATA fseBitMask<>+96(SB)/4, $16777215
DATA fseBitMask<>+100(SB)/4, $33554431
DATA fseBitMask<>+104(SB)/4, $67108863
DATA fseBitMask<>+108(SB)/4, $134217727
DATA fseBitMask<>+112(SB)/4, $268435455
DATA fseBitMask<>+116(SB)/4, $536870911
DATA fseBitMask<>+120(SB)/4, $1073741823
DATA fseBitMask<>+124(SB)/4, $2147483647
GLOBL fseBitMask<>(SB), RODATA|NOPTR, $128

TEXT ·compressUsingCTableAsm(SB), NOSPLIT, $64-56
	MOVQ dst+0(FP), DI
	MOVQ dstSize+8(FP), SI
	MOVQ src+16(FP), DX
	MOVQ srcSize+24(FP), CX
	MOVQ ct+32(FP), R8
	MOVQ fast+40(FP), R9
	MOVQ DX, 0x20(SP)                        // ed5a movq   %rdx,-0x20(%rsp)
	CMPQ CX, $2                              // ed5f cmpq   $0x2,%rcx
	JLS Cf098                                // ed63 jbe    f098 <FSE_compress_usingCTable_generic+0x348>
	LEAQ -0x8(DI)(SI*1), AX                  // ed69 leaq   -0x8(%rdi,%rsi,1),%rax
	MOVQ DI, R14                             // ed6e movq   %rdi,%r14
	MOVQ AX, 0x28(SP)                        // ed71 movq   %rax,-0x18(%rsp)
	CMPQ SI, $8                              // ed76 cmpq   $0x8,%rsi
	JLS Cf098                                // ed7a jbe    f098 <FSE_compress_usingCTable_generic+0x348>
	MOVQ CX, BX                              // ed80 movq   %rcx,%rbx
	LEAQ (DX)(CX*1), SI                      // ed83 leaq   (%rdx,%rcx,1),%rsi
	MOVWLZX (R8), R15                        // ed87 movzwl (%r8),%r15d
	MOVQ CX, DI                              // ed8b movq   %rcx,%rdi
	ANDL $1, BX                              // ed8e andl   $0x1,%ebx
	MOVBLZX -0x1(SI), AX                     // ed91 movzbl -0x1(%rsi),%eax
	LEAQ 0x4(R8), BP                         // ed95 leaq   0x4(%r8),%rbp
	MOVL $8, R11                             // ed99 movl   $0x8,%r11d
	MOVQ BX, 0x18(SP)                        // ed9f movq   %rbx,-0x28(%rsp)
	JEQ Cf0a8                                // eda4 je     f0a8 <FSE_compress_usingCTable_generic+0x358>
	TESTL R15, R15                           // edaa testl  %r15d,%r15d
	JEQ Cedc5                                // edad je     edc5 <FSE_compress_usingCTable_generic+0x75>
	LEAL -0x1(R15), CX                       // edaf leal   -0x1(%r15),%ecx
	MOVL $1, DX                              // edb3 movl   $0x1,%edx
	SHLL CX, DX                              // edb8 shll   %cl,%edx
	MOVLQSX DX, DX                           // edba movslq %edx,%rdx
	LEAQ 0x4(DX*4), R11                      // edbd leaq   0x4(,%rdx,4),%r11

Cedc5:
	ADDQ R8, R11                             // edc5 addq   %r8,%r11
	LEAQ fseBitMask<>(SB), BX               // edc8 leaq   BIT_mask(%rip),%rbx
	LEAQ (R11)(AX*8), DX                     // edcf leaq   (%r11,%rax,8),%rdx
	MOVL 0x4(DX), R8                         // edd3 movl   0x4(%rdx),%r8d
	MOVLQSX (DX), DX                         // edd7 movslq (%rdx),%rdx
	LEAL 0x8000(R8), CX                      // edda leal   0x8000(%r8),%ecx
	MOVL CX, AX                              // ede1 movl   %ecx,%eax
	SHRL $16, CX                             // ede3 shrl   $0x10,%ecx
	XORW AX, AX                              // ede6 xorw   %ax,%ax
	SUBL R8, AX                              // ede9 subl   %r8d,%eax
	SARQ CX, AX                              // edec sarq   %cl,%rax
	ADDQ DX, AX                              // edef addq   %rdx,%rax
	MOVWLZX (BP)(AX*2), DX                   // edf2 movzwl 0x0(%rbp,%rax,2),%edx
	MOVBLZX -0x2(SI), AX                     // edf7 movzbl -0x2(%rsi),%eax
	LEAQ (R11)(AX*8), R8                     // edfb leaq   (%r11,%rax,8),%r8
	MOVL 0x4(R8), R10                        // edff movl   0x4(%r8),%r10d
	LEAL 0x8000(R10), CX                     // ee03 leal   0x8000(%r10),%ecx
	MOVL CX, AX                              // ee0a movl   %ecx,%eax
	SHRL $16, CX                             // ee0c shrl   $0x10,%ecx
	XORW AX, AX                              // ee0f xorw   %ax,%ax
	SUBL R10, AX                             // ee12 subl   %r10d,%eax
	LEAQ -0x3(SI), R10                       // ee15 leaq   -0x3(%rsi),%r10
	SARQ CX, AX                              // ee19 sarq   %cl,%rax
	MOVLQSX (R8), CX                         // ee1c movslq (%r8),%rcx
	ADDQ CX, AX                              // ee1f addq   %rcx,%rax
	MOVBLZX -0x3(SI), CX                     // ee22 movzbl -0x3(%rsi),%ecx
	MOVWLZX (BP)(AX*2), AX                   // ee26 movzwl 0x0(%rbp,%rax,2),%eax
	LEAQ (R11)(CX*8), R12                    // ee2b leaq   (%r11,%rcx,8),%r12
	MOVL 0x4(R12), SI                        // ee2f movl   0x4(%r12),%esi
	ADDQ DX, SI                              // ee34 addq   %rdx,%rsi
	SARQ $16, SI                             // ee37 sarq   $0x10,%rsi
	MOVL (BX)(SI*4), R8                      // ee3b movl   (%rbx,%rsi,4),%r8d
	MOVL SI, CX                              // ee3f movl   %esi,%ecx
	ANDQ DX, R8                              // ee41 andq   %rdx,%r8
	SARQ CX, DX                              // ee44 sarq   %cl,%rdx
	MOVLQSX (R12), CX                        // ee47 movslq (%r12),%rcx
	MOVL SI, R12                             // ee4b movl   %esi,%r12d
	SHRL $3, R12                             // ee4e shrl   $0x3,%r12d
	MOVQ R8, R13                             // ee52 movq   %r8,%r13
	ANDL $7, SI                              // ee55 andl   $0x7,%esi
	ADDQ CX, DX                              // ee58 addq   %rcx,%rdx
	MOVL R12, CX                             // ee5b movl   %r12d,%ecx
	ADDQ R14, CX                             // ee5e addq   %r14,%rcx
	MOVWLZX (BP)(DX*2), DX                   // ee61 movzwl 0x0(%rbp,%rdx,2),%edx
	MOVQ R8, (R14)                           // ee66 movq   %r8,(%r14)
	MOVQ CX, 0x10(SP)                        // ee69 movq   %rcx,-0x30(%rsp)
	LEAL (R12*8), CX                         // ee6e leal   0x0(,%r12,8),%ecx
	SHRQ CX, R13                             // ee76 shrq   %cl,%r13
	MOVQ R13, 0x18(SP)                       // ee79 movq   %r13,-0x28(%rsp)
	TESTL R9, R9                             // ee7e testl  %r9d,%r9d
	JEQ Cf338                                // ee81 je     f338 <FSE_compress_usingCTable_generic+0x5e8>

Cee87:
	ANDL $2, DI                              // ee87 andl   $0x2,%edi
	JEQ Cf135                                // ee8a je     f135 <FSE_compress_usingCTable_generic+0x3e5>

Cee90:
	CMPQ 0x20(SP), R10                       // ee90 cmpq   %r10,-0x20(%rsp)
	JCC Cefeb                                // ee95 jae    efeb <FSE_compress_usingCTable_generic+0x29b>
	MOVL R15, 0x34(SP)                       // ee9b movl   %r15d,-0xc(%rsp)
	MOVQ R14, 0x38(SP)                       // eea0 movq   %r14,-0x8(%rsp)
	TESTL R9, R9                             // eea5 testl  %r9d,%r9d
	JNE Cf200                                // eea8 jne    f200 <FSE_compress_usingCTable_generic+0x4b0>
	// eeae xchgw  %ax,%ax

Ceeb0:
	MOVBLZX -0x1(R10), CX                    // eeb0 movzbl -0x1(%r10),%ecx
	MOVQ AX, R14                             // eeb5 movq   %rax,%r14
	MOVQ DX, R12                             // eeb8 movq   %rdx,%r12
	SUBQ $4, R10                             // eebb subq   $0x4,%r10
	LEAQ (R11)(CX*8), CX                     // eebf leaq   (%r11,%rcx,8),%rcx
	MOVL 0x4(CX), R8                         // eec3 movl   0x4(%rcx),%r8d
	MOVLQSX (CX), DI                         // eec7 movslq (%rcx),%rdi
	ADDQ AX, R8                              // eeca addq   %rax,%r8
	SARQ $16, R8                             // eecd sarq   $0x10,%r8
	MOVL R8, CX                              // eed1 movl   %r8d,%ecx
	LEAL (SI)(R8*1), R15                     // eed4 leal   (%rsi,%r8,1),%r15d
	MOVL R8, R8                              // eed8 movl   %r8d,%r8d
	SARQ CX, R14                             // eedb sarq   %cl,%r14
	MOVBLZX 0x2(R10), CX                     // eede movzbl 0x2(%r10),%ecx
	ADDQ R14, DI                             // eee3 addq   %r14,%rdi
	LEAQ (R11)(CX*8), R9                     // eee6 leaq   (%r11,%rcx,8),%r9
	MOVWLZX (BP)(DI*2), R13                  // eeea movzwl 0x0(%rbp,%rdi,2),%r13d
	MOVL 0x4(R9), CX                         // eef0 movl   0x4(%r9),%ecx
	MOVLQSX (R9), R9                         // eef4 movslq (%r9),%r9
	ADDQ DX, CX                              // eef7 addq   %rdx,%rcx
	SARQ $16, CX                             // eefa sarq   $0x10,%rcx
	SARQ CX, R12                             // eefe sarq   %cl,%r12
	LEAL (R15)(CX*1), DI                     // ef01 leal   (%r15,%rcx,1),%edi
	MOVL CX, CX                              // ef05 movl   %ecx,%ecx
	ADDQ R12, R9                             // ef07 addq   %r12,%r9
	MOVL (BX)(CX*4), CX                      // ef0a movl   (%rbx,%rcx,4),%ecx
	MOVWLZX (BP)(R9*2), R12                  // ef0d movzwl 0x0(%rbp,%r9,2),%r12d
	MOVBLZX 0x1(R10), R9                     // ef13 movzbl 0x1(%r10),%r9d
	ANDQ CX, DX                              // ef18 andq   %rcx,%rdx
	MOVL R15, CX                             // ef1b movl   %r15d,%ecx
	LEAQ (R11)(R9*8), R14                    // ef1e leaq   (%r11,%r9,8),%r14
	SHLQ CX, DX                              // ef22 shlq   %cl,%rdx
	MOVL (BX)(R8*4), CX                      // ef25 movl   (%rbx,%r8,4),%ecx
	MOVL 0x4(R14), R9                        // ef29 movl   0x4(%r14),%r9d
	ANDQ CX, AX                              // ef2d andq   %rcx,%rax
	MOVL SI, CX                              // ef30 movl   %esi,%ecx
	ADDQ R13, R9                             // ef32 addq   %r13,%r9
	SHLQ CX, AX                              // ef35 shlq   %cl,%rax
	MOVL DI, CX                              // ef38 movl   %edi,%ecx
	SARQ $16, R9                             // ef3a sarq   $0x10,%r9
	ORQ AX, DX                               // ef3e orq    %rax,%rdx
	MOVLQSX (R14), AX                        // ef41 movslq (%r14),%rax
	MOVL (BX)(R9*4), SI                      // ef44 movl   (%rbx,%r9,4),%esi
	ADDL R9, DI                              // ef48 addl   %r9d,%edi
	ANDQ R13, SI                             // ef4b andq   %r13,%rsi
	SHLQ CX, SI                              // ef4e shlq   %cl,%rsi
	MOVL R9, CX                              // ef51 movl   %r9d,%ecx
	SARQ CX, R13                             // ef54 sarq   %cl,%r13
	MOVBLZX (R10), CX                        // ef57 movzbl (%r10),%ecx
	ORQ SI, DX                               // ef5b orq    %rsi,%rdx
	ADDQ R13, AX                             // ef5e addq   %r13,%rax
	LEAQ (R11)(CX*8), R9                     // ef61 leaq   (%r11,%rcx,8),%r9
	MOVL DI, CX                              // ef65 movl   %edi,%ecx
	MOVWLZX (BP)(AX*2), AX                   // ef67 movzwl 0x0(%rbp,%rax,2),%eax
	MOVL 0x4(R9), SI                         // ef6c movl   0x4(%r9),%esi
	ADDQ R12, SI                             // ef70 addq   %r12,%rsi
	SARQ $16, SI                             // ef73 sarq   $0x10,%rsi
	MOVL (BX)(SI*4), R8                      // ef77 movl   (%rbx,%rsi,4),%r8d
	ADDL SI, DI                              // ef7b addl   %esi,%edi
	ANDQ R12, R8                             // ef7d andq   %r12,%r8
	SHLQ CX, R8                              // ef80 shlq   %cl,%r8
	MOVL SI, CX                              // ef83 movl   %esi,%ecx
	MOVL DI, SI                              // ef85 movl   %edi,%esi
	ORQ DX, R8                               // ef87 orq    %rdx,%r8
	MOVQ 0x18(SP), DX                        // ef8a movq   -0x28(%rsp),%rdx
	SARQ CX, R12                             // ef8f sarq   %cl,%r12
	ORQ DX, R8                               // ef92 orq    %rdx,%r8
	MOVLQSX (R9), DX                         // ef95 movslq (%r9),%rdx
	MOVL DI, R9                              // ef98 movl   %edi,%r9d
	SHRL $3, R9                              // ef9b shrl   $0x3,%r9d
	MOVQ R8, R14                             // ef9f movq   %r8,%r14
	ADDQ R12, DX                             // efa2 addq   %r12,%rdx
	ANDL $7, SI                              // efa5 andl   $0x7,%esi
	ANDL $-8, DI                             // efa8 andl   $0xfffffff8,%edi
	MOVL DI, CX                              // efab movl   %edi,%ecx
	MOVQ 0x28(SP), DI                        // efad movq   -0x18(%rsp),%rdi
	MOVWLZX (BP)(DX*2), DX                   // efb2 movzwl 0x0(%rbp,%rdx,2),%edx
	SHRQ CX, R14                             // efb7 shrq   %cl,%r14
	MOVQ 0x10(SP), CX                        // efba movq   -0x30(%rsp),%rcx
	MOVQ R14, 0x18(SP)                       // efbf movq   %r14,-0x28(%rsp)
	ADDQ CX, R9                              // efc4 addq   %rcx,%r9
	MOVQ R8, (CX)                            // efc7 movq   %r8,(%rcx)
	CMPQ DI, R9                              // efca cmpq   %r9,%rdi
	CMOVQLS DI, R9                           // efcd cmovbeq %rdi,%r9
	MOVQ R9, 0x10(SP)                        // efd1 movq   %r9,-0x30(%rsp)
	CMPQ 0x20(SP), R10                       // efd6 cmpq   %r10,-0x20(%rsp)
	JCS Ceeb0                                // efdb jb     eeb0 <FSE_compress_usingCTable_generic+0x160>

Cefe1:
	MOVL 0x34(SP), R15                       // efe1 movl   -0xc(%rsp),%r15d
	MOVQ 0x38(SP), R14                       // efe6 movq   -0x8(%rsp),%r14

Cefeb:
	MOVL R15, CX                             // efeb movl   %r15d,%ecx
	MOVL (BX)(CX*4), DI                      // efee movl   (%rbx,%rcx,4),%edi
	MOVL SI, CX                              // eff1 movl   %esi,%ecx
	MOVQ 0x18(SP), BX                        // eff3 movq   -0x28(%rsp),%rbx
	ADDL R15, SI                             // eff8 addl   %r15d,%esi
	MOVL SI, R9                              // effb movl   %esi,%r9d
	ANDQ DI, AX                              // effe andq   %rdi,%rax
	SHRL $3, R9                              // f001 shrl   $0x3,%r9d
	SHLQ CX, AX                              // f005 shlq   %cl,%rax
	MOVL R9, CX                              // f008 movl   %r9d,%ecx
	ORQ BX, AX                               // f00b orq    %rbx,%rax
	MOVQ 0x10(SP), BX                        // f00e movq   -0x30(%rsp),%rbx
	MOVQ AX, (BX)                            // f013 movq   %rax,(%rbx)
	LEAQ (BX)(CX*1), R8                      // f016 leaq   (%rbx,%rcx,1),%r8
	MOVQ 0x28(SP), BX                        // f01a movq   -0x18(%rsp),%rbx
	CMPQ BX, R8                              // f01f cmpq   %r8,%rbx
	CMOVQLS BX, R8                           // f022 cmovbeq %rbx,%r8
	ANDL $7, SI                              // f026 andl   $0x7,%esi
	ANDQ DI, DX                              // f029 andq   %rdi,%rdx
	MOVL SI, CX                              // f02c movl   %esi,%ecx
	ADDL R15, SI                             // f02e addl   %r15d,%esi
	SHLQ CX, DX                              // f031 shlq   %cl,%rdx
	LEAL (R9*8), CX                          // f034 leal   0x0(,%r9,8),%ecx
	SHRQ CX, AX                              // f03c shrq   %cl,%rax
	MOVL SI, CX                              // f03f movl   %esi,%ecx
	SHRL $3, CX                              // f041 shrl   $0x3,%ecx
	ORQ AX, DX                               // f044 orq    %rax,%rdx
	MOVL CX, AX                              // f047 movl   %ecx,%eax
	MOVQ DX, (R8)                            // f049 movq   %rdx,(%r8)
	ADDQ R8, AX                              // f04c addq   %r8,%rax
	CMPQ BX, AX                              // f04f cmpq   %rax,%rbx
	CMOVQLS BX, AX                           // f052 cmovbeq %rbx,%rax
	SHLL $3, CX                              // f056 shll   $0x3,%ecx
	ANDL $7, SI                              // f059 andl   $0x7,%esi
	SHRQ CX, DX                              // f05c shrq   %cl,%rdx
	LEAL 0x1(SI), DI                         // f05f leal   0x1(%rsi),%edi
	BTSQ SI, DX                              // f062 btsq   %rsi,%rdx
	MOVQ DX, (AX)                            // f066 movq   %rdx,(%rax)
	MOVL DI, DX                              // f069 movl   %edi,%edx
	SHRL $3, DX                              // f06b shrl   $0x3,%edx
	ADDQ DX, AX                              // f06e addq   %rdx,%rax
	CMPQ BX, AX                              // f071 cmpq   %rax,%rbx
	JCS Cf098                                // f074 jb     f098 <FSE_compress_usingCTable_generic+0x348>
	ANDL $7, DI                              // f076 andl   $0x7,%edi
	CMPQ BX, AX                              // f079 cmpq   %rax,%rbx
	JEQ Cf098                                // f07c je     f098 <FSE_compress_usingCTable_generic+0x348>
	SUBQ R14, AX                             // f07e subq   %r14,%rax
	CMPL DI, $1                              // f081 cmpl   $0x1,%edi
	SBBQ $-1, AX                             // f086 sbbq   $0xffffffffffffffff,%rax
	MOVQ AX, ret+48(FP)
	RET                                      // f092 retq
	// f093 nopl   0x0(%rax,%rax,1)

Cf098:
	XORL AX, AX                              // f099 xorl   %eax,%eax
	MOVQ AX, ret+48(FP)
	RET                                      // f0a4 retq
	// f0a5 nopl   (%rax)

Cf0a8:
	TESTW R15, R15                           // f0a8 testw  %r15w,%r15w
	JEQ Cf0c4                                // f0ac je     f0c4 <FSE_compress_usingCTable_generic+0x374>
	LEAL -0x1(R15), CX                       // f0ae leal   -0x1(%r15),%ecx
	MOVL $1, DX                              // f0b2 movl   $0x1,%edx
	SHLL CX, DX                              // f0b7 shll   %cl,%edx
	MOVLQSX DX, DX                           // f0b9 movslq %edx,%rdx
	LEAQ 0x4(DX*4), R11                      // f0bc leaq   0x4(,%rdx,4),%r11

Cf0c4:
	ADDQ R8, R11                             // f0c4 addq   %r8,%r11
	MOVQ R14, 0x10(SP)                       // f0c7 movq   %r14,-0x30(%rsp)
	LEAQ (R11)(AX*8), DX                     // f0cc leaq   (%r11,%rax,8),%rdx
	MOVL 0x4(DX), R10                        // f0d0 movl   0x4(%rdx),%r10d
	MOVLQSX (DX), DX                         // f0d4 movslq (%rdx),%rdx
	LEAL 0x8000(R10), CX                     // f0d7 leal   0x8000(%r10),%ecx
	MOVL CX, AX                              // f0de movl   %ecx,%eax
	SHRL $16, CX                             // f0e0 shrl   $0x10,%ecx
	XORW AX, AX                              // f0e3 xorw   %ax,%ax
	SUBL R10, AX                             // f0e6 subl   %r10d,%eax
	LEAQ -0x2(SI), R10                       // f0e9 leaq   -0x2(%rsi),%r10
	SARQ CX, AX                              // f0ed sarq   %cl,%rax
	ADDQ DX, AX                              // f0f0 addq   %rdx,%rax
	MOVBLZX -0x2(SI), DX                     // f0f3 movzbl -0x2(%rsi),%edx
	MOVWLZX 0x4(R8)(AX*2), AX                // f0f7 movzwl 0x4(%r8,%rax,2),%eax
	LEAQ (R11)(DX*8), SI                     // f0fd leaq   (%r11,%rdx,8),%rsi
	MOVL 0x4(SI), BX                         // f101 movl   0x4(%rsi),%ebx
	LEAL 0x8000(BX), CX                      // f104 leal   0x8000(%rbx),%ecx
	MOVL CX, DX                              // f10a movl   %ecx,%edx
	SHRL $16, CX                             // f10c shrl   $0x10,%ecx
	XORW DX, DX                              // f10f xorw   %dx,%dx
	SUBL BX, DX                              // f112 subl   %ebx,%edx
	LEAQ fseBitMask<>(SB), BX               // f114 leaq   BIT_mask(%rip),%rbx
	SARQ CX, DX                              // f11b sarq   %cl,%rdx
	MOVLQSX (SI), CX                         // f11e movslq (%rsi),%rcx
	XORL SI, SI                              // f121 xorl   %esi,%esi
	ADDQ CX, DX                              // f123 addq   %rcx,%rdx
	ANDL $2, DI                              // f126 andl   $0x2,%edi
	MOVWLZX 0x4(R8)(DX*2), DX                // f129 movzwl 0x4(%r8,%rdx,2),%edx
	JNE Cee90                                // f12f jne    ee90 <FSE_compress_usingCTable_generic+0x140>

Cf135:
	MOVBLZX -0x1(R10), CX                    // f135 movzbl -0x1(%r10),%ecx
	LEAQ (R11)(CX*8), R12                    // f13a leaq   (%r11,%rcx,8),%r12
	MOVL SI, CX                              // f13e movl   %esi,%ecx
	MOVL 0x4(R12), DI                        // f140 movl   0x4(%r12),%edi
	ADDQ AX, DI                              // f145 addq   %rax,%rdi
	SARQ $16, DI                             // f148 sarq   $0x10,%rdi
	MOVL (BX)(DI*4), R8                      // f14c movl   (%rbx,%rdi,4),%r8d
	ADDL DI, SI                              // f150 addl   %edi,%esi
	ANDQ AX, R8                              // f152 andq   %rax,%r8
	SHLQ CX, R8                              // f155 shlq   %cl,%r8
	MOVL DI, CX                              // f158 movl   %edi,%ecx
	SARQ CX, AX                              // f15a sarq   %cl,%rax
	MOVLQSX (R12), CX                        // f15d movslq (%r12),%rcx
	LEAQ -0x2(R10), R12                      // f161 leaq   -0x2(%r10),%r12
	MOVQ R8, R13                             // f165 movq   %r8,%r13
	ADDQ CX, AX                              // f168 addq   %rcx,%rax
	MOVBLZX -0x2(R10), CX                    // f16b movzbl -0x2(%r10),%ecx
	MOVWLZX (BP)(AX*2), AX                   // f170 movzwl 0x0(%rbp,%rax,2),%eax
	LEAQ (R11)(CX*8), R10                    // f175 leaq   (%r11,%rcx,8),%r10
	MOVL SI, CX                              // f179 movl   %esi,%ecx
	MOVL 0x4(R10), R8                        // f17b movl   0x4(%r10),%r8d
	ADDQ DX, R8                              // f17f addq   %rdx,%r8
	SARQ $16, R8                             // f182 sarq   $0x10,%r8
	MOVL (BX)(R8*4), DI                      // f186 movl   (%rbx,%r8,4),%edi
	ADDL R8, SI                              // f18a addl   %r8d,%esi
	ANDQ DX, DI                              // f18d andq   %rdx,%rdi
	SHLQ CX, DI                              // f190 shlq   %cl,%rdi
	MOVQ 0x18(SP), CX                        // f193 movq   -0x28(%rsp),%rcx
	ORQ R13, DI                              // f198 orq    %r13,%rdi
	MOVQ 0x10(SP), R13                       // f19b movq   -0x30(%rsp),%r13
	ORQ CX, DI                               // f1a0 orq    %rcx,%rdi
	MOVL R8, CX                              // f1a3 movl   %r8d,%ecx
	SARQ CX, DX                              // f1a6 sarq   %cl,%rdx
	MOVLQSX (R10), CX                        // f1a9 movslq (%r10),%rcx
	MOVQ DI, R10                             // f1ac movq   %rdi,%r10
	ADDQ CX, DX                              // f1af addq   %rcx,%rdx
	MOVL SI, CX                              // f1b2 movl   %esi,%ecx
	ANDL $7, SI                              // f1b4 andl   $0x7,%esi
	SHRL $3, CX                              // f1b7 shrl   $0x3,%ecx
	MOVWLZX (BP)(DX*2), DX                   // f1ba movzwl 0x0(%rbp,%rdx,2),%edx
	MOVL CX, R8                              // f1bf movl   %ecx,%r8d
	SHLL $3, CX                              // f1c2 shll   $0x3,%ecx
	SHRQ CX, R10                             // f1c5 shrq   %cl,%r10
	ADDQ R13, R8                             // f1c8 addq   %r13,%r8
	MOVQ R10, 0x18(SP)                       // f1cb movq   %r10,-0x28(%rsp)
	TESTL R9, R9                             // f1d0 testl  %r9d,%r9d
	JEQ Cf358                                // f1d3 je     f358 <FSE_compress_usingCTable_generic+0x608>
	MOVQ DI, (R13)                           // f1d9 movq   %rdi,0x0(%r13)
	MOVQ R12, R10                            // f1dd movq   %r12,%r10
	MOVQ R8, 0x10(SP)                        // f1e0 movq   %r8,-0x30(%rsp)
	CMPQ 0x20(SP), R12                       // f1e5 cmpq   %r12,-0x20(%rsp)
	JCC Cefeb                                // f1ea jae    efeb <FSE_compress_usingCTable_generic+0x29b>
	MOVL R15, 0x34(SP)                       // f1f0 movl   %r15d,-0xc(%rsp)
	MOVQ R14, 0x38(SP)                       // f1f5 movq   %r14,-0x8(%rsp)
	// f1fa nopw   0x0(%rax,%rax,1)

Cf200:
	MOVBLZX -0x1(R10), CX                    // f200 movzbl -0x1(%r10),%ecx
	MOVQ AX, R14                             // f205 movq   %rax,%r14
	MOVQ DX, R12                             // f208 movq   %rdx,%r12
	SUBQ $4, R10                             // f20b subq   $0x4,%r10
	LEAQ (R11)(CX*8), R8                     // f20f leaq   (%r11,%rcx,8),%r8
	MOVL 0x4(R8), DI                         // f213 movl   0x4(%r8),%edi
	MOVLQSX (R8), R8                         // f217 movslq (%r8),%r8
	ADDQ AX, DI                              // f21a addq   %rax,%rdi
	SARQ $16, DI                             // f21d sarq   $0x10,%rdi
	MOVL DI, CX                              // f221 movl   %edi,%ecx
	LEAL (SI)(DI*1), R15                     // f223 leal   (%rsi,%rdi,1),%r15d
	MOVL DI, DI                              // f227 movl   %edi,%edi
	SARQ CX, R14                             // f229 sarq   %cl,%r14
	MOVQ R14, CX                             // f22c movq   %r14,%rcx
	ADDQ R8, CX                              // f22f addq   %r8,%rcx
	MOVWLZX (BP)(CX*2), R9                   // f232 movzwl 0x0(%rbp,%rcx,2),%r9d
	MOVBLZX 0x2(R10), CX                     // f238 movzbl 0x2(%r10),%ecx
	LEAQ (R11)(CX*8), R8                     // f23d leaq   (%r11,%rcx,8),%r8
	MOVL 0x4(R8), CX                         // f241 movl   0x4(%r8),%ecx
	MOVLQSX (R8), R8                         // f245 movslq (%r8),%r8
	ADDQ DX, CX                              // f248 addq   %rdx,%rcx
	SARQ $16, CX                             // f24b sarq   $0x10,%rcx
	SARQ CX, R12                             // f24f sarq   %cl,%r12
	LEAL (R15)(CX*1), R14                    // f252 leal   (%r15,%rcx,1),%r14d
	MOVL CX, CX                              // f256 movl   %ecx,%ecx
	ADDQ R8, R12                             // f258 addq   %r8,%r12
	MOVL (BX)(CX*4), CX                      // f25b movl   (%rbx,%rcx,4),%ecx
	MOVWLZX (BP)(R12*2), R8                  // f25e movzwl 0x0(%rbp,%r12,2),%r8d
	MOVBLZX 0x1(R10), R12                    // f264 movzbl 0x1(%r10),%r12d
	ANDQ CX, DX                              // f269 andq   %rcx,%rdx
	MOVL R15, CX                             // f26c movl   %r15d,%ecx
	MOVQ 0x10(SP), R15                       // f26f movq   -0x30(%rsp),%r15
	LEAQ (R11)(R12*8), R13                   // f274 leaq   (%r11,%r12,8),%r13
	SHLQ CX, DX                              // f278 shlq   %cl,%rdx
	MOVL (BX)(DI*4), CX                      // f27b movl   (%rbx,%rdi,4),%ecx
	MOVL 0x4(R13), R12                       // f27e movl   0x4(%r13),%r12d
	ANDQ CX, AX                              // f282 andq   %rcx,%rax
	MOVL SI, CX                              // f285 movl   %esi,%ecx
	ADDQ R9, R12                             // f287 addq   %r9,%r12
	SHLQ CX, AX                              // f28a shlq   %cl,%rax
	MOVL R14, CX                             // f28d movl   %r14d,%ecx
	SARQ $16, R12                            // f290 sarq   $0x10,%r12
	ORQ AX, DX                               // f294 orq    %rax,%rdx
	MOVLQSX (R13), AX                        // f297 movslq 0x0(%r13),%rax
	MOVL (BX)(R12*4), SI                     // f29b movl   (%rbx,%r12,4),%esi
	ANDQ R9, SI                              // f29f andq   %r9,%rsi
	SHLQ CX, SI                              // f2a2 shlq   %cl,%rsi
	MOVL R12, CX                             // f2a5 movl   %r12d,%ecx
	SARQ CX, R9                              // f2a8 sarq   %cl,%r9
	MOVBLZX (R10), CX                        // f2ab movzbl (%r10),%ecx
	ORQ SI, DX                               // f2af orq    %rsi,%rdx
	LEAL (R14)(R12*1), SI                    // f2b2 leal   (%r14,%r12,1),%esi
	ADDQ AX, R9                              // f2b6 addq   %rax,%r9
	LEAQ (R11)(CX*8), R12                    // f2b9 leaq   (%r11,%rcx,8),%r12
	MOVWLZX (BP)(R9*2), AX                   // f2bd movzwl 0x0(%rbp,%r9,2),%eax
	MOVL SI, CX                              // f2c3 movl   %esi,%ecx
	MOVL 0x4(R12), R9                        // f2c5 movl   0x4(%r12),%r9d
	ADDQ R8, R9                              // f2ca addq   %r8,%r9
	SARQ $16, R9                             // f2cd sarq   $0x10,%r9
	MOVL (BX)(R9*4), DI                      // f2d1 movl   (%rbx,%r9,4),%edi
	ADDL R9, SI                              // f2d5 addl   %r9d,%esi
	ANDQ R8, DI                              // f2d8 andq   %r8,%rdi
	SHLQ CX, DI                              // f2db shlq   %cl,%rdi
	MOVL R9, CX                              // f2de movl   %r9d,%ecx
	ORQ DX, DI                               // f2e1 orq    %rdx,%rdi
	MOVQ 0x18(SP), DX                        // f2e4 movq   -0x28(%rsp),%rdx
	SARQ CX, R8                              // f2e9 sarq   %cl,%r8
	MOVL SI, CX                              // f2ec movl   %esi,%ecx
	SHRL $3, CX                              // f2ee shrl   $0x3,%ecx
	ORQ DX, DI                               // f2f1 orq    %rdx,%rdi
	MOVLQSX (R12), DX                        // f2f4 movslq (%r12),%rdx
	MOVL CX, R9                              // f2f8 movl   %ecx,%r9d
	ADDQ DX, R8                              // f2fb addq   %rdx,%r8
	MOVWLZX (BP)(R8*2), DX                   // f2fe movzwl 0x0(%rbp,%r8,2),%edx
	MOVQ R15, R8                             // f304 movq   %r15,%r8
	ADDQ R9, R15                             // f307 addq   %r9,%r15
	ANDL $7, SI                              // f30a andl   $0x7,%esi
	MOVQ R15, 0x10(SP)                       // f30d movq   %r15,-0x30(%rsp)
	SHLL $3, CX                              // f312 shll   $0x3,%ecx
	MOVQ DI, R15                             // f315 movq   %rdi,%r15
	SHRQ CX, R15                             // f318 shrq   %cl,%r15
	MOVQ DI, (R8)                            // f31b movq   %rdi,(%r8)
	MOVQ R15, 0x18(SP)                       // f31e movq   %r15,-0x28(%rsp)
	CMPQ 0x20(SP), R10                       // f323 cmpq   %r10,-0x20(%rsp)
	JCS Cf200                                // f328 jb     f200 <FSE_compress_usingCTable_generic+0x4b0>
	JMP Cefe1                                // f32e jmpq   efe1 <FSE_compress_usingCTable_generic+0x291>
	// f333 nopl   0x0(%rax,%rax,1)

Cf338:
	MOVQ 0x10(SP), CX                        // f338 movq   -0x30(%rsp),%rcx
	MOVQ 0x28(SP), R8                        // f33d movq   -0x18(%rsp),%r8
	CMPQ CX, R8                              // f342 cmpq   %r8,%rcx
	CMOVQHI R8, CX                           // f345 cmovaq %r8,%rcx
	MOVQ CX, 0x10(SP)                        // f349 movq   %rcx,-0x30(%rsp)
	JMP Cee87                                // f34e jmpq   ee87 <FSE_compress_usingCTable_generic+0x137>
	// f353 nopl   0x0(%rax,%rax,1)

Cf358:
	MOVQ DI, (R13)                           // f358 movq   %rdi,0x0(%r13)
	MOVQ 0x28(SP), DI                        // f35c movq   -0x18(%rsp),%rdi
	MOVQ R12, R10                            // f361 movq   %r12,%r10
	CMPQ DI, R8                              // f364 cmpq   %r8,%rdi
	CMOVQLS DI, R8                           // f367 cmovbeq %rdi,%r8
	MOVQ R8, 0x10(SP)                        // f36b movq   %r8,-0x30(%rsp)
	CMPQ 0x20(SP), R12                       // f370 cmpq   %r12,-0x20(%rsp)
	JCC Cefeb                                // f375 jae    efeb <FSE_compress_usingCTable_generic+0x29b>
	MOVL R15, 0x34(SP)                       // f37b movl   %r15d,-0xc(%rsp)
	MOVQ R14, 0x38(SP)                       // f380 movq   %r14,-0x8(%rsp)
	JMP Ceeb0                                // f385 jmpq   eeb0 <FSE_compress_usingCTable_generic+0x160>
	// f38a nopw   0x0(%rax,%rax,1)

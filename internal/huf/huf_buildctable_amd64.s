//go:build amd64 && !appengine && !noasm && !purego && gc

// buildCTableAsm is gcc's HUF_buildCTable_wksp from ref/zstd/programs/zstd
// (zstd v1.5.7, 0x15010-0x15b20), transliterated instruction for instruction
// by Task 20k. HUF_sort, HUF_buildTree, HUF_setMaxHeight and
// HUF_buildCTableFromTree are inlined in it as gcc inlined them; the bucket
// quicksort is hufSimpleQuickSort below. Each line keeps C's address and
// instruction in its comment. Registers are C's (rax = AX, ..., r15 = R15),
// and so are the frame offsets: C's locals at 0x08-0xa7(SP), with C's
// workspace (HUF_buildCTable_wksp_tables: huffNodeTbl[512] at 0, the
// rankPosition table at 0x1000) at 0xc0(SP).
//
// Departures from gcc's code:
//   - no endbr64, register pushes or stack canary (Go's prologue saves BP;
//     the other registers are scratch in ABI0);
//   - the arguments come from the Go frame, and workSpace/wkspSize are the
//     frame's workspace and its size, so C's alignment and size checks pass;
//   - MOVOU/MOVUPS for C's movdqa/movaps: Go does not 16-align SP;
//   - an all-zero histogram returns -2 (C scans below huffNode0 there,
//     undefined behaviour; buildCTable panics as the Go body does);
//   - an infeasible maxNbBits (more than 2^maxNbBits nonzero symbols)
//     returns -3 where setMaxHeight would index huffNode with noSymbol
//     (C: undefined behaviour; buildCTable runs the Go body, which panics);
//   - the call to HUF_simpleQuickSort keeps C's calling convention (DI = arr,
//     SI = low, DX = high; BX, BP and R12-R15 preserved).
//
// Go arguments (FP offsets, checked by go vet's asmdecl): ct+0, count+8,
// maxSymbolValue+16, maxNbBits+20, ret+24.

#include "textflag.h"

// rankLast's initial value in HUF_setMaxHeight (noSymbol, 0xF0F0F0F0).
DATA hufNoSymbol<>+0(SB)/8, $0xf0f0f0f0f0f0f0f0
DATA hufNoSymbol<>+8(SB)/8, $0xf0f0f0f0f0f0f0f0
GLOBL hufNoSymbol<>(SB), RODATA|NOPTR, $16

TEXT ·buildCTableAsm(SB), 0, $5056-32
	MOVQ ct+0(FP), DI
	MOVQ count+8(FP), SI
	MOVL maxSymbolValue+16(FP), DX
	MOVL maxNbBits+20(FP), CX
	LEAQ 0xc0(SP), R8 // workSpace
	MOVQ $0x1300, R9  // wkspSize
	MOVL DX, R15                             // 15016 movl   %edx,%r15d
	MOVL DX, 0x14(SP)                        // 15028 movl   %edx,0x14(%rsp)
	MOVQ $-66, DX                            // 1502c movq   $0xffffffffffffffbe,%rdx
	MOVQ DI, 0x8(SP)                         // 15033 movq   %rdi,0x8(%rsp)
	MOVQ R8, AX                              // 15049 movq   %r8,%rax
	NEGQ AX                                  // 1504c negq   %rax
	ANDL $3, AX                              // 1504f andl   $0x3,%eax
	CMPQ R9, AX                              // 15052 cmpq   %rax,%r9
	JCS L158f5                               // 15055 jb     158f5 <HUF_buildCTable_wksp+0x8e5>
	SUBQ AX, R9                              // 1505b subq   %rax,%r9
	CMPQ R9, $0x12ff                         // 1505e cmpq   $0x12ff,%r9
	JLS L158f5                               // 15065 jbe    158f5 <HUF_buildCTable_wksp+0x8e5>
	MOVL $11, DX                             // 1506b movl   $0xb,%edx
	MOVL CX, R12                             // 15070 movl   %ecx,%r12d
	TESTL CX, CX                             // 15073 testl  %ecx,%ecx
	CMOVLEQ DX, R12                          // 15075 cmovel %edx,%r12d
	MOVQ $-46, DX                            // 15079 movq   $0xffffffffffffffd2,%rdx
	CMPL R15, $255                           // 15080 cmpl   $0xff,%r15d
	JHI L158f5                               // 15087 ja     158f5 <HUF_buildCTable_wksp+0x8e5>
	LEAQ (R8)(AX*1), BP                      // 1508d leaq   (%r8,%rax,1),%rbp
	XORL AX, AX                              // 15091 xorl   %eax,%eax
	LEAQ 0x8(BP), BX                         // 15093 leaq   0x8(%rbp),%rbx
	MOVQ BP, CX                              // 15097 movq   %rbp,%rcx
	LEAQ 0x1008(BP), DX                      // 1509a leaq   0x1008(%rbp),%rdx
	MOVQ $0, (BP)                            // 150a1 movq   $0x0,0x0(%rbp)
	MOVQ BX, DI                              // 150a9 movq   %rbx,%rdi
	LEAQ 0x1000(BP), R8                      // 150ac leaq   0x1000(%rbp),%r8
	MOVQ $0, 0xff8(BP)                       // 150b3 movq   $0x0,0xff8(%rbp)
	ANDQ $-8, DI                             // 150be andq   $0xfffffffffffffff8,%rdi
	SUBQ DI, CX                              // 150c2 subq   %rdi,%rcx
	ADDL $0x1000, CX                         // 150c5 addl   $0x1000,%ecx
	SHRL $3, CX                              // 150cb shrl   $0x3,%ecx
	REP; STOSQ                               // 150ce rep stosq %rax,%es:(%rdi)
	MOVQ DX, DI                              // 150d1 movq   %rdx,%rdi
	MOVQ R8, CX                              // 150d4 movq   %r8,%rcx
	MOVQ SI, DX                              // 150d7 movq   %rsi,%rdx
	MOVQ $0, 0x1000(BP)                      // 150da movq   $0x0,0x1000(%rbp)
	ANDQ $-8, DI                             // 150e5 andq   $0xfffffffffffffff8,%rdi
	MOVQ $0, 0x12f8(BP)                      // 150e9 movq   $0x0,0x12f8(%rbp)
	SUBQ DI, CX                              // 150f4 subq   %rdi,%rcx
	ADDL $768, CX                            // 150f7 addl   $0x300,%ecx
	SHRL $3, CX                              // 150fd shrl   $0x3,%ecx
	REP; STOSQ                               // 15100 rep stosq %rax,%es:(%rdi)
	MOVL R15, AX                             // 15103 movl   %r15d,%eax
	MOVL $189, DI                            // 15106 movl   $0xbd,%edi
	MOVQ AX, 0x18(SP)                        // 1510b movq   %rax,0x18(%rsp)
	LEAQ 0x4(SI)(AX*4), CX                   // 15110 leaq   0x4(%rsi,%rax,4),%rcx
	// 15115 nopl   (%rax)

L15118:
	MOVL (DX), AX                            // 15118 movl   (%rdx),%eax
	CMPL AX, $164                            // 1511a cmpl   $0xa4,%eax
	JLS L15928                               // 1511f jbe    15928 <HUF_buildCTable_wksp+0x918>
	BSRL AX, AX                              // 15125 bsrl   %eax,%eax
	MOVL DI, R9                              // 15128 movl   %edi,%r9d
	ADDQ $4, DX                              // 1512b addq   $0x4,%rdx
	XORL $31, AX                             // 1512f xorl   $0x1f,%eax
	SUBL AX, R9                              // 15132 subl   %eax,%r9d
	ADDW $1, (R8)(R9*4)                      // 15135 addw   $0x1,(%r8,%r9,4)
	CMPQ DX, CX                              // 1513b cmpq   %rcx,%rdx
	JNE L15118                               // 1513e jne    15118 <HUF_buildCTable_wksp+0x108>

L15140:
	MOVWLZX 0x12fc(BP), DX                   // 15140 movzwl 0x12fc(%rbp),%edx
	LEAQ 0x12f8(BP), AX                      // 15147 leaq   0x12f8(%rbp),%rax
	LEAQ 0xffc(BP), CX                       // 1514e leaq   0xffc(%rbp),%rcx
	// 15155 nopl   (%rax)

L15158:
	ADDW (AX), DX                            // 15158 addw   (%rax),%dx
	SUBQ $4, AX                              // 1515b subq   $0x4,%rax
	MOVL DX, X1                              // 1515f movd   %edx,%xmm1
	PSHUFLW $0, X1, X0                       // 15163 pshuflw $0x0,%xmm1,%xmm0
	MOVL X0, 0x4(AX)                         // 15168 movd   %xmm0,0x4(%rax)
	CMPQ CX, AX                              // 1516d cmpq   %rax,%rcx
	JNE L15158                               // 15170 jne    15158 <HUF_buildCTable_wksp+0x148>
	MOVQ 0x18(SP), R11                       // 15172 movq   0x18(%rsp),%r11
	XORL AX, AX                              // 15177 xorl   %eax,%eax
	MOVL $189, R9                            // 15179 movl   $0xbd,%r9d
	JMP L1518b                               // 1517f jmp    1518b <HUF_buildCTable_wksp+0x17b>
	// 15181 nopl   0x0(%rax)

L15188:
	MOVQ DX, AX                              // 15188 movq   %rdx,%rax

L1518b:
	MOVL (SI)(AX*4), DX                      // 1518b movl   (%rsi,%rax,4),%edx
	MOVL DX, CX                              // 1518e movl   %edx,%ecx
	CMPL DX, $164                            // 15190 cmpl   $0xa4,%edx
	JLS L151a3                               // 15196 jbe    151a3 <HUF_buildCTable_wksp+0x193>
	BSRL DX, DI                              // 15198 bsrl   %edx,%edi
	MOVL R9, CX                              // 1519b movl   %r9d,%ecx
	XORL $31, DI                             // 1519e xorl   $0x1f,%edi
	SUBL DI, CX                              // 151a1 subl   %edi,%ecx

L151a3:
	ADDL $1, CX                              // 151a3 addl   $0x1,%ecx
	LEAQ (R8)(CX*4), DI                      // 151a6 leaq   (%r8,%rcx,4),%rdi
	MOVWLZX 0x2(DI), CX                      // 151aa movzwl 0x2(%rdi),%ecx
	LEAL 0x1(CX), R10                        // 151ae leal   0x1(%rcx),%r10d
	LEAQ (BX)(CX*8), CX                      // 151b2 leaq   (%rbx,%rcx,8),%rcx
	MOVW R10, 0x2(DI)                        // 151b6 movw   %r10w,0x2(%rdi)
	MOVL DX, (CX)                            // 151bb movl   %edx,(%rcx)
	LEAQ 0x1(AX), DX                         // 151bd leaq   0x1(%rax),%rdx
	MOVB AL, 0x6(CX)                         // 151c1 movb   %al,0x6(%rcx)
	CMPQ R11, AX                             // 151c4 cmpq   %rax,%r11
	JNE L15188                               // 151c7 jne    15188 <HUF_buildCTable_wksp+0x178>
	LEAQ 0x1294(BP), R13                     // 151c9 leaq   0x1294(%rbp),%r13
	LEAQ 0x12fc(BP), R14                     // 151d0 leaq   0x12fc(%rbp),%r14
	// 151d7 nopw   0x0(%rax,%rax,1)

L151e0:
	MOVWLZX (R13), CX                        // 151e0 movzwl 0x0(%r13),%ecx
	MOVWLZX 0x2(R13), DX                     // 151e5 movzwl 0x2(%r13),%edx
	SUBL CX, DX                              // 151ea subl   %ecx,%edx
	CMPL DX, $1                              // 151ec cmpl   $0x1,%edx
	JLE L151ff                               // 151ef jle    151ff <HUF_buildCTable_wksp+0x1ef>
	SUBL $1, DX                              // 151f1 subl   $0x1,%edx
	LEAQ (BX)(CX*8), DI                      // 151f4 leaq   (%rbx,%rcx,8),%rdi
	XORL SI, SI                              // 151f8 xorl   %esi,%esi
	CALL hufSimpleQuickSort<>(SB)            // 151fa callq HUF_simpleQuickSort

L151ff:
	ADDQ $4, R13                             // 151ff addq   $0x4,%r13
	CMPQ R14, R13                            // 15203 cmpq   %r13,%r14
	JNE L151e0                               // 15206 jne    151e0 <HUF_buildCTable_wksp+0x1d0>
	MOVQ 0x18(SP), AX                        // 15208 movq   0x18(%rsp),%rax
	MOVL 0x14(SP), SI                        // 1520d movl   0x14(%rsp),%esi
	SHLQ $3, AX                              // 15211 shlq   $0x3,%rax
	LEAQ (BX)(AX*1), DI                      // 15215 leaq   (%rbx,%rax,1),%rdi
	MOVL (DI), DX                            // 15219 movl   (%rdi),%edx
	TESTL DX, DX                             // 1521b testl  %edx,%edx
	JNE L15ace                               // 1521d jne    15ace <HUF_buildCTable_wksp+0xabe>
	CMPL (BX), $0                            // (not in C) every count zero
	JEQ allzero
	ADDQ BP, AX                              // 15223 addq   %rbp,%rax
	// 15226 cs nopw 0x0(%rax,%rax,1)

L15230:
	MOVQ AX, CX                              // 15230 movq   %rax,%rcx
	MOVL (AX), DX                            // 15233 movl   (%rax),%edx
	SUBQ $8, AX                              // 15235 subq   $0x8,%rax
	SUBL $1, SI                              // 15239 subl   $0x1,%esi
	TESTL DX, DX                             // 1523c testl  %edx,%edx
	JEQ L15230                               // 1523e je     15230 <HUF_buildCTable_wksp+0x220>
	MOVQ CX, AX                              // 15240 movq   %rcx,%rax
	MOVQ CX, DI                              // 15243 movq   %rcx,%rdi
	MOVL SI, R11                             // 15246 movl   %esi,%r11d
	SUBQ BP, AX                              // 15249 subq   %rbp,%rax
	SUBQ $8, AX                              // 1524c subq   $0x8,%rax

L15250:
	ADDQ BP, AX                              // 15250 addq   %rbp,%rax
	LEAL 0xff(SI), CX                        // 15253 leal   0xff(%rsi),%ecx
	MOVL $256, R8                            // 15259 movl   $0x100,%r8d
	ADDL (AX), DX                            // 1525f addl   (%rax),%edx
	LEAL -0x2(SI), R9                        // 15261 leal   -0x2(%rsi),%r9d
	MOVL DX, 0x808(BP)                       // 15265 movl   %edx,0x808(%rbp)
	MOVL $256, DX                            // 1526b movl   $0x100,%edx
	MOVW DX, 0x4(AX)                         // 15270 movw   %dx,0x4(%rax)
	MOVLQSX CX, AX                           // 15274 movslq %ecx,%rax
	LEAQ (AX*8), R13                         // 15277 leaq   0x0(,%rax,8),%r13
	MOVW R8, 0x4(DI)                         // 1527f movw   %r8w,0x4(%rdi)
	LEAQ (BX)(R13*1), R8                     // 15284 leaq   (%rbx,%r13,1),%r8
	CMPL CX, $256                            // 15288 cmpl   $0x100,%ecx
	JLE L15a9f                               // 1528e jle    15a9f <HUF_buildCTable_wksp+0xa8f>
	MOVL R9, R15                             // 15294 movl   %r9d,%r15d
	LEAQ 0x810(BP), AX                       // 15297 leaq   0x810(%rbp),%rax
	LEAQ (R15*8), CX                         // 1529e leaq   0x0(,%r15,8),%rcx
	LEAQ 0x818(BP)(CX*1), DX                 // 152a6 leaq   0x818(%rbp,%rcx,1),%rdx
	MOVQ DX, DI                              // 152ae movq   %rdx,%rdi
	SUBQ AX, DI                              // 152b1 subq   %rax,%rdi
	ANDL $8, DI                              // 152b4 andl   $0x8,%edi
	JEQ L152d0                               // 152b7 je     152d0 <HUF_buildCTable_wksp+0x2c0>
	MOVL $0x40000000, (AX)                   // 152b9 movl   $0x40000000,(%rax)
	LEAQ 0x818(BP), AX                       // 152bf leaq   0x818(%rbp),%rax
	CMPQ DX, AX                              // 152c6 cmpq   %rax,%rdx
	JEQ L152e6                               // 152c9 je     152e6 <HUF_buildCTable_wksp+0x2d6>
	// 152cb nopl   0x0(%rax,%rax,1)

L152d0:
	MOVL $0x40000000, (AX)                   // 152d0 movl   $0x40000000,(%rax)
	ADDQ $16, AX                             // 152d6 addq   $0x10,%rax
	MOVL $0x40000000, -0x8(AX)               // 152da movl   $0x40000000,-0x8(%rax)
	CMPQ DX, AX                              // 152e1 cmpq   %rax,%rdx
	JNE L152d0                               // 152e4 jne    152d0 <HUF_buildCTable_wksp+0x2c0>

L152e6:
	MOVL SI, 0x28(SP)                        // 152e6 movl   %esi,0x28(%rsp)
	MOVL $257, DX                            // 152ea movl   $0x101,%edx
	ADDQ $258, R15                           // 152ef addq   $0x102,%r15
	MOVL $256, R10                           // 152f6 movl   $0x100,%r10d
	MOVL $-0x80000000, (BP)                  // 152fc movl   $0x80000000,0x0(%rbp)
	MOVQ CX, 0x20(SP)                        // 15303 movq   %rcx,0x20(%rsp)
	MOVL R11, 0x2c(SP)                       // 15308 movl   %r11d,0x2c(%rsp)
	MOVQ R13, 0x30(SP)                       // 1530d movq   %r13,0x30(%rsp)
	MOVQ R8, 0x38(SP)                        // 15312 movq   %r8,0x38(%rsp)
	// 15317 nopw   0x0(%rax,%rax,1)

L15320:
	MOVLQSX R9, AX                           // 15320 movslq %r9d,%rax
	MOVLQSX R10, DI                          // 15323 movslq %r10d,%rdi
	SHLQ $3, AX                              // 15326 shlq   $0x3,%rax
	SHLQ $3, DI                              // 1532a shlq   $0x3,%rdi
	LEAQ (BX)(AX*1), CX                      // 1532e leaq   (%rbx,%rax,1),%rcx
	LEAQ (BX)(DI*1), R11                     // 15332 leaq   (%rbx,%rdi,1),%r11
	MOVL (CX), SI                            // 15336 movl   (%rcx),%esi
	MOVL (R11), R13                          // 15338 movl   (%r11),%r13d
	CMPL SI, R13                             // 1533b cmpl   %r13d,%esi
	JCC L15968                               // 1533e jae    15968 <HUF_buildCTable_wksp+0x958>
	ADDQ BP, AX                              // 15344 addq   %rbp,%rax
	SUBL $1, R9                              // 15347 subl   $0x1,%r9d
	MOVL R13, DI                             // 1534b movl   %r13d,%edi
	MOVQ R11, R14                            // 1534e movq   %r11,%r14
	MOVL (AX), R8                            // 15351 movl   (%rax),%r8d

L15354:
	CMPL R8, DI                              // 15354 cmpl   %edi,%r8d
	JCC L15940                               // 15357 jae    15940 <HUF_buildCTable_wksp+0x930>
	ADDL SI, R8                              // 1535d addl   %esi,%r8d
	SUBL $1, R9                              // 15360 subl   $0x1,%r9d
	MOVL R8, 0x8(BP)(DX*8)                   // 15364 movl   %r8d,0x8(%rbp,%rdx,8)
	MOVW DX, 0x4(AX)                         // 15369 movw   %dx,0x4(%rax)
	MOVW DX, 0x4(CX)                         // 1536d movw   %dx,0x4(%rcx)
	ADDQ $1, DX                              // 15371 addq   $0x1,%rdx
	CMPQ DX, R15                             // 15375 cmpq   %r15,%rdx
	JNE L15320                               // 15378 jne    15320 <HUF_buildCTable_wksp+0x310>

L1537a:
	MOVQ 0x30(SP), R13                       // 1537a movq   0x30(%rsp),%r13
	MOVQ 0x38(SP), R8                        // 1537f movq   0x38(%rsp),%r8
	MOVQ 0x20(SP), CX                        // 15384 movq   0x20(%rsp),%rcx
	MOVL 0x28(SP), SI                        // 15389 movl   0x28(%rsp),%esi
	LEAQ -0x7f8(R13), DX                     // 1538d leaq   -0x7f8(%r13),%rdx
	MOVB $0, 0x7(R8)                         // 15394 movb   $0x0,0x7(%r8)
	MOVL 0x2c(SP), R11                       // 15399 movl   0x2c(%rsp),%r11d
	LEAQ -0x8(BP)(DX*1), DI                  // 1539e leaq   -0x8(%rbp,%rdx,1),%rdi
	LEAQ (BP)(DX*1), AX                      // 153a3 leaq   0x0(%rbp,%rdx,1),%rax
	SUBQ CX, DI                              // 153a8 subq   %rcx,%rdi
	// 153ab nopl   0x0(%rax,%rax,1)

L153b0:
	MOVWLZX 0x7fc(AX), DX                    // 153b0 movzwl 0x7fc(%rax),%edx
	SUBQ $8, AX                              // 153b7 subq   $0x8,%rax
	MOVBLZX 0x7(BX)(DX*8), DX                // 153bb movzbl 0x7(%rbx,%rdx,8),%edx
	ADDL $1, DX                              // 153c0 addl   $0x1,%edx
	MOVB DL, 0x807(AX)                       // 153c3 movb   %dl,0x807(%rax)
	CMPQ AX, DI                              // 153c9 cmpq   %rdi,%rax
	JNE L153b0                               // 153cc jne    153b0 <HUF_buildCTable_wksp+0x3a0>

L153ce:
	MOVLQSX SI, DX                           // 153ce movslq %esi,%rdx
	LEAQ 0xc(BP), AX                         // 153d1 leaq   0xc(%rbp),%rax
	LEAQ 0x14(BP)(DX*8), CX                  // 153d5 leaq   0x14(%rbp,%rdx,8),%rcx
	// 153da nopw   0x0(%rax,%rax,1)

L153e0:
	MOVWLZX (AX), DX                         // 153e0 movzwl (%rax),%edx
	ADDQ $8, AX                              // 153e3 addq   $0x8,%rax
	MOVBLZX 0x7(BX)(DX*8), DX                // 153e7 movzbl 0x7(%rbx,%rdx,8),%edx
	ADDL $1, DX                              // 153ec addl   $0x1,%edx
	MOVB DL, -0x5(AX)                        // 153ef movb   %dl,-0x5(%rax)
	CMPQ CX, AX                              // 153f2 cmpq   %rax,%rcx
	JNE L153e0                               // 153f5 jne    153e0 <HUF_buildCTable_wksp+0x3d0>

L153f7:
	MOVL R11, AX                             // 153f7 movl   %r11d,%eax
	MOVBLZX 0x7(BX)(AX*8), R8                // 153fa movzbl 0x7(%rbx,%rax,8),%r8d
	CMPL R12, R8                             // 15400 cmpl   %r8d,%r12d
	JCC L156d9                               // 15403 jae    156d9 <HUF_buildCTable_wksp+0x6c9>
	LEAQ -0x7f8(R13), R10                    // 15409 leaq   -0x7f8(%r13),%r10
	MOVL R8, AX                              // 15410 movl   %r8d,%eax
	MOVL $1, R11                             // 15413 movl   $0x1,%r11d
	LEAQ (BX)(R10*1), R15                    // 15419 leaq   (%rbx,%r10,1),%r15
	SUBL R12, AX                             // 1541d subl   %r12d,%eax
	MOVBLZX 0x7(R15), R9                     // 15420 movzbl 0x7(%r15),%r9d
	MOVL AX, CX                              // 15425 movl   %eax,%ecx
	MOVL AX, 0x28(SP)                        // 15427 movl   %eax,0x28(%rsp)
	SHLL CX, R11                             // 1542b shll   %cl,%r11d
	CMPL R12, R9                             // 1542e cmpl   %r9d,%r12d
	JCC L15af2                               // 15431 jae    15af2 <HUF_buildCTable_wksp+0xae2>
	MOVL R12, R14                            // 15437 movl   %r12d,%r14d
	ADDQ BP, R10                             // 1543a addq   %rbp,%r10
	MOVL SI, DX                              // 1543d movl   %esi,%edx
	XORL DI, DI                              // 1543f xorl   %edi,%edi
	XORL AX, AX                              // 15441 xorl   %eax,%eax
	MOVL $1, R13                             // 15443 movl   $0x1,%r13d
	// 15449 nopl   0x0(%rax)

L15450:
	MOVL R8, CX                              // 15450 movl   %r8d,%ecx
	ADDL R11, AX                             // 15453 addl   %r11d,%eax
	MOVB R14B, 0x7(R15)(DI*1)                // 15456 movb   %r14b,0x7(%r15,%rdi,1)
	SUBL $1, DX                              // 1545b subl   $0x1,%edx
	SUBL R9, CX                              // 1545e subl   %r9d,%ecx
	MOVL R13, R9                             // 15461 movl   %r13d,%r9d
	SHLL CX, R9                              // 15464 shll   %cl,%r9d
	SUBL R9, AX                              // 15467 subl   %r9d,%eax
	MOVBLZX 0x7(R10)(DI*1), R9               // 1546a movzbl 0x7(%r10,%rdi,1),%r9d
	SUBQ $8, DI                              // 15470 subq   $0x8,%rdi
	CMPL R12, R9                             // 15474 cmpl   %r9d,%r12d
	JCS L15450                               // 15477 jb     15450 <HUF_buildCTable_wksp+0x440>
	MOVBLZX 0x28(SP), CX                     // 15479 movzbl 0x28(%rsp),%ecx
	SARL CX, AX                              // 1547e sarl   %cl,%eax

L15480:
	CMPL R12, R9                             // 15480 cmpl   %r9d,%r12d
	JNE L154a1                               // 15483 jne    154a1 <HUF_buildCTable_wksp+0x491>
	SUBL $1, DX                              // 15485 subl   $0x1,%edx
	MOVLQSX DX, CX                           // 15488 movslq %edx,%rcx
	// 1548b nopl   0x0(%rax,%rax,1)

L15490:
	MOVBLZX 0xf(BP)(CX*8), DI                // 15490 movzbl 0xf(%rbp,%rcx,8),%edi
	MOVQ CX, DX                              // 15495 movq   %rcx,%rdx
	SUBQ $1, CX                              // 15498 subq   $0x1,%rcx
	CMPL R12, DI                             // 1549c cmpl   %edi,%r12d
	JEQ L15490                               // 1549f je     15490 <HUF_buildCTable_wksp+0x480>

L154a1:
	MOVOU hufNoSymbol<>(SB), X0              // 154a1 movdqa 0xfade7(%rip),%xmm0
	MOVLQSX DX, CX                           // 154a9 movslq %edx,%rcx
	MOVL R12, R8                             // 154ac movl   %r12d,%r8d
	MOVQ $-0xf0f0f0f0f0f0f10, DI             // 154af movabsq $0xf0f0f0f0f0f0f0f0,%rdi
	MOVQ DI, 0xa0(SP)                        // 154b9 movq   %rdi,0xa0(%rsp)
	MOVUPS X0, 0x70(SP)                      // 154c1 movaps %xmm0,0x70(%rsp)
	MOVUPS X0, 0x80(SP)                      // 154c6 movaps %xmm0,0x80(%rsp)
	MOVUPS X0, 0x90(SP)                      // 154ce movaps %xmm0,0x90(%rsp)
	TESTL DX, DX                             // 154d6 testl  %edx,%edx
	JMI L15500                               // 154d8 js     15500 <HUF_buildCTable_wksp+0x4f0>
	// 154da nopw   0x0(%rax,%rax,1)

L154e0:
	MOVBLZX 0xf(BP)(CX*8), DI                // 154e0 movzbl 0xf(%rbp,%rcx,8),%edi
	CMPL DI, R8                              // 154e5 cmpl   %r8d,%edi
	JCC L154f8                               // 154e8 jae    154f8 <HUF_buildCTable_wksp+0x4e8>
	MOVL R12, R8                             // 154ea movl   %r12d,%r8d
	SUBL DI, R8                              // 154ed subl   %edi,%r8d
	MOVL CX, 0x70(SP)(R8*4)                  // 154f0 movl   %ecx,0x70(%rsp,%r8,4)
	MOVL DI, R8                              // 154f5 movl   %edi,%r8d

L154f8:
	SUBQ $1, CX                              // 154f8 subq   $0x1,%rcx
	TESTL CX, CX                             // 154fc testl  %ecx,%ecx
	JPL L154e0                               // 154fe jns    154e0 <HUF_buildCTable_wksp+0x4d0>

L15500:
	TESTL AX, AX                             // 15500 testl  %eax,%eax
	JLE L156a9                               // 15502 jle    156a9 <HUF_buildCTable_wksp+0x699>
	LEAQ 0x70(SP), R13                       // 15508 leaq   0x70(%rsp),%r13
	MOVL $31, R15                            // 1550d movl   $0x1f,%r15d
	// 15513 nopl   0x0(%rax,%rax,1)

L15518:
	BSRL AX, DI                              // 15518 bsrl   %eax,%edi
	MOVL $32, CX                             // 1551b movl   $0x20,%ecx
	XORL $31, DI                             // 15520 xorl   $0x1f,%edi
	SUBL DI, CX                              // 15523 subl   %edi,%ecx
	CMPL CX, $1                              // 15525 cmpl   $0x1,%ecx
	JEQ L15581                               // 15528 je     15581 <HUF_buildCTable_wksp+0x571>
	MOVL CX, R8                              // 1552a movl   %ecx,%r8d
	MOVL R15, R9                             // 1552d movl   %r15d,%r9d
	SUBL DI, R9                              // 15530 subl   %edi,%r9d
	MOVL 0x70(SP)(R8*4), R8                  // 15533 movl   0x70(%rsp,%r8,4),%r8d
	LEAQ (R13)(R9*4), R14                    // 15538 leaq   0x0(%r13,%r9,4),%r14
	// 1553d nopl   (%rax)

L15540:
	MOVL R8, DI                              // 15540 movl   %r8d,%edi
	MOVL CX, R11                             // 15543 movl   %ecx,%r11d
	MOVL (R14), R8                           // 15546 movl   (%r14),%r8d
	SUBL $1, CX                              // 15549 subl   $0x1,%ecx
	CMPL DI, $-0xf0f0f10                     // 1554c cmpl   $0xf0f0f0f0,%edi
	JEQ L15578                               // 15552 je     15578 <HUF_buildCTable_wksp+0x568>
	MOVL DI, R10                             // 15554 movl   %edi,%r10d
	CMPL R8, $-0xf0f0f10                     // 15557 cmpl   $0xf0f0f0f0,%r8d
	JEQ L15988                               // 1555e je     15988 <HUF_buildCTable_wksp+0x978>
	MOVL R8, R9                              // 15564 movl   %r8d,%r9d
	MOVL (BX)(R9*8), R9                      // 15567 movl   (%rbx,%r9,8),%r9d
	ADDL R9, R9                              // 1556b addl   %r9d,%r9d
	CMPL R9, (BX)(R10*8)                     // 1556e cmpl   (%rbx,%r10,8),%r9d
	JCC L15988                               // 15572 jae    15988 <HUF_buildCTable_wksp+0x978>

L15578:
	SUBQ $4, R14                             // 15578 subq   $0x4,%r14
	CMPL CX, $1                              // 1557c cmpl   $0x1,%ecx
	JNE L15540                               // 1557f jne    15540 <HUF_buildCTable_wksp+0x530>

L15581:
	MOVL 0x74(SP), DI                        // 15581 movl   0x74(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 15585 cmpl   $0xf0f0f0f0,%edi
	JNE L15b03                               // 1558b jne    15b03 <HUF_buildCTable_wksp+0xaf3>
	MOVL 0x78(SP), DI                        // 15591 movl   0x78(%rsp),%edi
	MOVL $2, R11                             // 15595 movl   $0x2,%r11d
	MOVL DI, R10                             // 1559b movl   %edi,%r10d
	CMPL DI, $-0xf0f0f10                     // 1559e cmpl   $0xf0f0f0f0,%edi
	JNE L15992                               // 155a4 jne    15992 <HUF_buildCTable_wksp+0x982>
	MOVL 0x7c(SP), DI                        // 155aa movl   0x7c(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 155ae cmpl   $0xf0f0f0f0,%edi
	JNE L15a3d                               // 155b4 jne    15a3d <HUF_buildCTable_wksp+0xa2d>
	MOVL 0x80(SP), DI                        // 155ba movl   0x80(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 155c1 cmpl   $0xf0f0f0f0,%edi
	JNE L15a4b                               // 155c7 jne    15a4b <HUF_buildCTable_wksp+0xa3b>
	MOVL 0x84(SP), DI                        // 155cd movl   0x84(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 155d4 cmpl   $0xf0f0f0f0,%edi
	JNE L15a59                               // 155da jne    15a59 <HUF_buildCTable_wksp+0xa49>
	MOVL 0x88(SP), DI                        // 155e0 movl   0x88(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 155e7 cmpl   $0xf0f0f0f0,%edi
	JNE L15a67                               // 155ed jne    15a67 <HUF_buildCTable_wksp+0xa57>
	MOVL 0x8c(SP), DI                        // 155f3 movl   0x8c(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 155fa cmpl   $0xf0f0f0f0,%edi
	JNE L15a75                               // 15600 jne    15a75 <HUF_buildCTable_wksp+0xa65>
	MOVL 0x90(SP), DI                        // 15606 movl   0x90(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 1560d cmpl   $0xf0f0f0f0,%edi
	JNE L15a83                               // 15613 jne    15a83 <HUF_buildCTable_wksp+0xa73>
	MOVL 0x94(SP), DI                        // 15619 movl   0x94(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 15620 cmpl   $0xf0f0f0f0,%edi
	JNE L15a91                               // 15626 jne    15a91 <HUF_buildCTable_wksp+0xa81>
	MOVL 0x98(SP), DI                        // 1562c movl   0x98(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 15633 cmpl   $0xf0f0f0f0,%edi
	JNE L15ac0                               // 15639 jne    15ac0 <HUF_buildCTable_wksp+0xab0>
	MOVL 0x9c(SP), DI                        // 1563f movl   0x9c(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 15646 cmpl   $0xf0f0f0f0,%edi
	JNE L15ad6                               // 1564c jne    15ad6 <HUF_buildCTable_wksp+0xac6>
	MOVL 0xa0(SP), DI                        // 15652 movl   0xa0(%rsp),%edi
	CMPL DI, $-0xf0f0f10                     // 15659 cmpl   $0xf0f0f0f0,%edi
	JNE L15ae4                               // 1565f jne    15ae4 <HUF_buildCTable_wksp+0xad4>
	MOVL 0xa4(SP), CX                        // 15665 movl   0xa4(%rsp),%ecx
	SUBL $0x1000, AX                         // 1566c subl   $0x1000,%eax
	MOVL $13, R11                            // 15671 movl   $0xd,%r11d
	MOVL $12, R8                             // 15677 movl   $0xc,%r8d
	CMPL CX, $-0xf0f0f10                     // (not in C) rankLast[12] == noSymbol: infeasible
	JEQ infeasible
	ADDB $1, 0x7(BX)(CX*8)                  // 1567d addb   $0x1,0x7(%rbx,%rcx,8)
	MOVQ CX, DI                              // 15682 movq   %rcx,%rdi

L15685:
	MOVL R11, CX                             // 15685 movl   %r11d,%ecx
	MOVL DI, 0x70(SP)(R8*4)                  // 15688 movl   %edi,0x70(%rsp,%r8,4)
	MOVL 0x70(SP)(CX*4), DI                  // 1568d movl   0x70(%rsp,%rcx,4),%edi
	TESTL DI, DI                             // 15691 testl  %edi,%edi
	JNE L159c8                               // 15693 jne    159c8 <HUF_buildCTable_wksp+0x9b8>

L15699:
	MOVL $-0xf0f0f10, 0x70(SP)(CX*4)         // 15699 movl   $0xf0f0f0f0,0x70(%rsp,%rcx,4)

L156a1:
	TESTL AX, AX                             // 156a1 testl  %eax,%eax
	JGT L15518                               // 156a3 jg     15518 <HUF_buildCTable_wksp+0x508>

L156a9:
	TESTL AX, AX                             // 156a9 testl  %eax,%eax
	JEQ L156d6                               // 156ab je     156d6 <HUF_buildCTable_wksp+0x6c6>
	MOVL 0x74(SP), CX                        // 156ad movl   0x74(%rsp),%ecx
	// 156b1 nopl   0x0(%rax)

L156b8:
	ADDL $1, AX                              // 156b8 addl   $0x1,%eax
	CMPL CX, $-0xf0f0f10                     // 156bb cmpl   $0xf0f0f0f0,%ecx
	JEQ L159f1                               // 156c1 je     159f1 <HUF_buildCTable_wksp+0x9e1>
	LEAL 0x1(CX), DI                         // 156c7 leal   0x1(%rcx),%edi
	SUBB $1, 0x7(BX)(DI*8)                   // 156ca subb   $0x1,0x7(%rbx,%rdi,8)
	MOVQ DI, CX                              // 156cf movq   %rdi,%rcx

L156d2:
	TESTL AX, AX                             // 156d2 testl  %eax,%eax
	JNE L156b8                               // 156d4 jne    156b8 <HUF_buildCTable_wksp+0x6a8>

L156d6:
	MOVL R12, R8                             // 156d6 movl   %r12d,%r8d

L156d9:
	MOVQ $-1, DX                             // 156d9 movq   $0xffffffffffffffff,%rdx
	CMPL R8, $12                             // 156e0 cmpl   $0xc,%r8d
	JHI L158f5                               // 156e4 ja     158f5 <HUF_buildCTable_wksp+0x8e5>
	MOVQ 0x8(SP), AX                         // 156ea movq   0x8(%rsp),%rax
	PXOR X0, X0                              // 156ef pxor   %xmm0,%xmm0
	MOVUPS X0, 0x50(SP)                      // 156f3 movaps %xmm0,0x50(%rsp)
	MOVUPS X0, 0x70(SP)                      // 156f8 movaps %xmm0,0x70(%rsp)
	LEAQ 0x8(AX), DX                         // 156fd leaq   0x8(%rax),%rdx
	MOVUPS X0, 0x5a(SP)                      // 15701 movups %xmm0,0x5a(%rsp)
	MOVUPS X0, 0x7a(SP)                      // 15706 movups %xmm0,0x7a(%rsp)
	TESTL SI, SI                             // 1570b testl  %esi,%esi
	JMI L15732                               // 1570d js     15732 <HUF_buildCTable_wksp+0x722>
	MOVLQSX SI, CX                           // 1570f movslq %esi,%rcx
	LEAQ 0xf(BP), AX                         // 15712 leaq   0xf(%rbp),%rax
	LEAQ 0x17(BP)(CX*8), SI                  // 15716 leaq   0x17(%rbp,%rcx,8),%rsi
	// 1571b nopl   0x0(%rax,%rax,1)

L15720:
	MOVBLZX (AX), CX                         // 15720 movzbl (%rax),%ecx
	ADDQ $8, AX                              // 15723 addq   $0x8,%rax
	ADDW $1, 0x50(SP)(CX*2)                  // 15727 addw   $0x1,0x50(%rsp,%rcx,2)
	CMPQ AX, SI                              // 1572d cmpq   %rsi,%rax
	JNE L15720                               // 15730 jne    15720 <HUF_buildCTable_wksp+0x710>

L15732:
	TESTL R8, R8                             // 15732 testl  %r8d,%r8d
	JEQ L15855                               // 15735 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX R8, AX                           // 1573b movslq %r8d,%rax
	MOVL R8, CX                              // 1573e movl   %r8d,%ecx
	MOVWLZX 0x50(SP)(AX*2), AX               // 15741 movzwl 0x50(%rsp,%rax,2),%eax
	SHRW $1, AX                              // 15746 shrw   $1,%ax
	SUBL $1, CX                              // 15749 subl   $0x1,%ecx
	JEQ L15855                               // 1574c je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 15752 movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 15755 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 1575a addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 1575f movl   %r8d,%ecx
	SHRW $1, AX                              // 15762 shrw   $1,%ax
	SUBL $2, CX                              // 15765 subl   $0x2,%ecx
	JEQ L15855                               // 15768 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 1576e movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 15771 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 15776 addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 1577b movl   %r8d,%ecx
	SHRW $1, AX                              // 1577e shrw   $1,%ax
	SUBL $3, CX                              // 15781 subl   $0x3,%ecx
	JEQ L15855                               // 15784 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 1578a movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 1578d movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 15792 addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 15797 movl   %r8d,%ecx
	SHRW $1, AX                              // 1579a shrw   $1,%ax
	SUBL $4, CX                              // 1579d subl   $0x4,%ecx
	JEQ L15855                               // 157a0 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 157a6 movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 157a9 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 157ae addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 157b3 movl   %r8d,%ecx
	SHRW $1, AX                              // 157b6 shrw   $1,%ax
	SUBL $5, CX                              // 157b9 subl   $0x5,%ecx
	JEQ L15855                               // 157bc je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 157c2 movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 157c5 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 157ca addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 157cf movl   %r8d,%ecx
	SHRW $1, AX                              // 157d2 shrw   $1,%ax
	SUBL $6, CX                              // 157d5 subl   $0x6,%ecx
	JEQ L15855                               // 157d8 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 157da movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 157dd movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 157e2 addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 157e7 movl   %r8d,%ecx
	SHRW $1, AX                              // 157ea shrw   $1,%ax
	SUBL $7, CX                              // 157ed subl   $0x7,%ecx
	JEQ L15855                               // 157f0 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 157f2 movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 157f5 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 157fa addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 157ff movl   %r8d,%ecx
	SHRW $1, AX                              // 15802 shrw   $1,%ax
	SUBL $8, CX                              // 15805 subl   $0x8,%ecx
	JEQ L15855                               // 15808 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 1580a movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 1580d movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 15812 addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 15817 movl   %r8d,%ecx
	SHRW $1, AX                              // 1581a shrw   $1,%ax
	SUBL $9, CX                              // 1581d subl   $0x9,%ecx
	JEQ L15855                               // 15820 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 15822 movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 15825 movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 1582a addw   0x50(%rsp,%rcx,2),%ax
	MOVL R8, CX                              // 1582f movl   %r8d,%ecx
	SHRW $1, AX                              // 15832 shrw   $1,%ax
	SUBL $10, CX                             // 15835 subl   $0xa,%ecx
	JEQ L15855                               // 15838 je     15855 <HUF_buildCTable_wksp+0x845>
	MOVLQSX CX, CX                           // 1583a movslq %ecx,%rcx
	MOVW AX, 0x70(SP)(CX*2)                  // 1583d movw   %ax,0x70(%rsp,%rcx,2)
	ADDW 0x50(SP)(CX*2), AX                  // 15842 addw   0x50(%rsp,%rcx,2),%ax
	SHRW $1, AX                              // 15847 shrw   $1,%ax
	CMPL R8, $11                             // 1584a cmpl   $0xb,%r8d
	JEQ L15855                               // 1584e je     15855 <HUF_buildCTable_wksp+0x845>
	MOVW AX, 0x72(SP)                        // 15850 movw   %ax,0x72(%rsp)

L15855:
	MOVQ 0x18(SP), SI                        // 15855 movq   0x18(%rsp),%rsi
	LEAQ 0xe(BP), AX                         // 1585a leaq   0xe(%rbp),%rax
	SHLQ $3, SI                              // 1585e shlq   $0x3,%rsi
	LEAQ 0x16(BP)(SI*1), DI                  // 15862 leaq   0x16(%rbp,%rsi,1),%rdi
	// 15867 nopw   0x0(%rax,%rax,1)

L15870:
	MOVBLZX (AX), CX                         // 15870 movzbl (%rax),%ecx
	MOVBLZX 0x1(AX), R9                      // 15873 movzbl 0x1(%rax),%r9d
	ADDQ $8, AX                              // 15878 addq   $0x8,%rax
	MOVQ R9, (DX)(CX*8)                      // 1587c movq   %r9,(%rdx,%rcx,8)
	CMPQ AX, DI                              // 15880 cmpq   %rdi,%rax
	JNE L15870                               // 15883 jne    15870 <HUF_buildCTable_wksp+0x860>
	MOVQ 0x8(SP), AX                         // 15885 movq   0x8(%rsp),%rax
	MOVL $64, R10                            // 1588a movl   $0x40,%r10d
	LEAQ 0x10(AX)(SI*1), R9                  // 15890 leaq   0x10(%rax,%rsi,1),%r9
	// 15895 nopl   (%rax)

L15898:
	MOVQ (DX), DI                            // 15898 movq   (%rdx),%rdi
	MOVBLZX DIB, SI                          // 1589b movzbl %dil,%esi
	MOVWLZX 0x70(SP)(SI*2), AX               // 1589f movzwl 0x70(%rsp,%rsi,2),%eax
	LEAL 0x1(AX), CX                         // 158a4 leal   0x1(%rax),%ecx
	MOVW CX, 0x70(SP)(SI*2)                  // 158a7 movw   %cx,0x70(%rsp,%rsi,2)
	TESTQ SI, SI                             // 158ac testq  %rsi,%rsi
	JEQ L158bf                               // 158af je     158bf <HUF_buildCTable_wksp+0x8af>
	MOVL R10, CX                             // 158b1 movl   %r10d,%ecx
	SUBL SI, CX                              // 158b4 subl   %esi,%ecx
	SHLQ CX, AX                              // 158b6 shlq   %cl,%rax
	ORQ DI, AX                               // 158b9 orq    %rdi,%rax
	MOVQ AX, (DX)                            // 158bc movq   %rax,(%rdx)

L158bf:
	ADDQ $8, DX                              // 158bf addq   $0x8,%rdx
	CMPQ DX, R9                              // 158c3 cmpq   %r9,%rdx
	JNE L15898                               // 158c6 jne    15898 <HUF_buildCTable_wksp+0x888>
	XORL AX, AX                              // 158c8 xorl   %eax,%eax
	MOVB R8B, 0x48(SP)                       // 158ca movb   %r8b,0x48(%rsp)
	MOVQ 0x8(SP), SI                         // 158cf movq   0x8(%rsp),%rsi
	MOVL R8, DX                              // 158d4 movl   %r8d,%edx
	MOVW AX, 0x4e(SP)                        // 158d7 movw   %ax,0x4e(%rsp)
	MOVBLZX 0x14(SP), AX                     // 158dc movzbl 0x14(%rsp),%eax
	MOVL $0, 0x4a(SP)                        // 158e1 movl   $0x0,0x4a(%rsp)
	MOVB AL, 0x49(SP)                        // 158e9 movb   %al,0x49(%rsp)
	MOVQ 0x48(SP), AX                        // 158ed movq   0x48(%rsp),%rax
	MOVQ AX, (SI)                            // 158f2 movq   %rax,(%rsi)

L158f5:
	MOVQ DX, ret+24(FP)                      // 15913 movq %rdx,%rax
	RET                                      // 15920 retq
	// 15921 nopl   0x0(%rax)

L15928:
	ADDQ $4, DX                              // 15928 addq   $0x4,%rdx
	ADDW $1, (R8)(AX*4)                      // 1592c addw   $0x1,(%r8,%rax,4)
	CMPQ CX, DX                              // 15932 cmpq   %rdx,%rcx
	JNE L15118                               // 15935 jne    15118 <HUF_buildCTable_wksp+0x108>
	JMP L15140                               // 1593b jmpq   15140 <HUF_buildCTable_wksp+0x130>

L15940:
	ADDL SI, DI                              // 15940 addl   %esi,%edi
	ADDL $1, R10                             // 15942 addl   $0x1,%r10d
	MOVL DI, 0x8(BP)(DX*8)                   // 15946 movl   %edi,0x8(%rbp,%rdx,8)
	MOVW DX, 0x4(R14)                        // 1594a movw   %dx,0x4(%r14)
	MOVW DX, 0x4(CX)                         // 1594f movw   %dx,0x4(%rcx)
	ADDQ $1, DX                              // 15953 addq   $0x1,%rdx
	CMPQ R15, DX                             // 15957 cmpq   %rdx,%r15
	JNE L15320                               // 1595a jne    15320 <HUF_buildCTable_wksp+0x310>
	JMP L1537a                               // 15960 jmpq   1537a <HUF_buildCTable_wksp+0x36a>
	// 15965 nopl   (%rax)

L15968:
	LEAQ 0x8(BX)(DI*1), R14                  // 15968 leaq   0x8(%rbx,%rdi,1),%r14
	MOVL SI, R8                              // 1596d movl   %esi,%r8d
	MOVQ CX, AX                              // 15970 movq   %rcx,%rax
	ADDL $1, R10                             // 15973 addl   $0x1,%r10d
	MOVL (R14), DI                           // 15977 movl   (%r14),%edi
	MOVL R13, SI                             // 1597a movl   %r13d,%esi
	MOVQ R11, CX                             // 1597d movq   %r11,%rcx
	JMP L15354                               // 15980 jmpq   15354 <HUF_buildCTable_wksp+0x344>
	// 15985 nopl   (%rax)

L15988:
	CMPL R11, $12                            // 15988 cmpl   $0xc,%r11d
	JHI L15a30                               // 1598c ja     15a30 <HUF_buildCTable_wksp+0xa20>

L15992:
	LEAL -0x1(R11), R8                       // 15992 leal   -0x1(%r11),%r8d
	MOVL 0x70(SP)(R8*4), R9                  // 15996 movl   0x70(%rsp,%r8,4),%r9d
	MOVQ R8, CX                              // 1599b movq   %r8,%rcx

L1599e:
	MOVL $1, R14                             // 1599e movl   $0x1,%r14d
	ADDB $1, 0x7(BX)(R10*8)                  // 159a4 addb   $0x1,0x7(%rbx,%r10,8)
	SHLL CX, R14                             // 159aa shll   %cl,%r14d
	SUBL R14, AX                             // 159ad subl   %r14d,%eax
	CMPL R9, $-0xf0f0f10                     // 159b0 cmpl   $0xf0f0f0f0,%r9d
	JEQ L15685                               // 159b7 je     15685 <HUF_buildCTable_wksp+0x675>
	MOVL R11, CX                             // 159bd movl   %r11d,%ecx
	TESTL DI, DI                             // 159c0 testl  %edi,%edi
	JEQ L15699                               // 159c2 je     15699 <HUF_buildCTable_wksp+0x689>

L159c8:
	LEAL -0x1(DI), R8                        // 159c8 leal   -0x1(%rdi),%r8d
	MOVBLZX 0x7(BX)(R8*8), R9                // 159cc movzbl 0x7(%rbx,%r8,8),%r9d
	MOVQ R8, DI                              // 159d2 movq   %r8,%rdi
	MOVL R12, R8                             // 159d5 movl   %r12d,%r8d
	SUBL R11, R8                             // 159d8 subl   %r11d,%r8d
	MOVL $-0xf0f0f10, R11                    // 159db movl   $0xf0f0f0f0,%r11d
	CMPL R9, R8                              // 159e1 cmpl   %r8d,%r9d
	CMOVLNE R11, DI                          // 159e4 cmovnel %r11d,%edi
	MOVL DI, 0x70(SP)(CX*4)                  // 159e8 movl   %edi,0x70(%rsp,%rcx,4)
	JMP L156a1                               // 159ec jmpq   156a1 <HUF_buildCTable_wksp+0x691>

L159f1:
	MOVLQSX DX, DI                           // 159f1 movslq %edx,%rdi
	MOVBLZX 0x7(BX)(DI*8), CX                // 159f4 movzbl 0x7(%rbx,%rdi,8),%ecx
	CMPL R12, CX                             // 159f9 cmpl   %ecx,%r12d
	JNE L15afb                               // 159fc jne    15afb <HUF_buildCTable_wksp+0xaeb>
	SUBL $1, DX                              // 15a02 subl   $0x1,%edx
	MOVLQSX DX, DX                           // 15a05 movslq %edx,%rdx
	// 15a08 nopl   0x0(%rax,%rax,1)

L15a10:
	MOVBLZX 0xf(BP)(DX*8), CX                // 15a10 movzbl 0xf(%rbp,%rdx,8),%ecx
	MOVQ DX, DI                              // 15a15 movq   %rdx,%rdi
	LEAQ -0x1(DX), DX                        // 15a18 leaq   -0x1(%rdx),%rdx
	CMPL R12, CX                             // 15a1c cmpl   %ecx,%r12d
	JEQ L15a10                               // 15a1f je     15a10 <HUF_buildCTable_wksp+0xa00>
	LEAL 0x1(DI), CX                         // 15a21 leal   0x1(%rdi),%ecx
	MOVL DI, DX                              // 15a24 movl   %edi,%edx

L15a26:
	SUBB $1, 0xf(BX)(DI*8)                   // 15a26 subb   $0x1,0xf(%rbx,%rdi,8)
	JMP L156d2                               // 15a2b jmpq   156d2 <HUF_buildCTable_wksp+0x6c2>

L15a30:
	MOVL CX, R8                              // 15a30 movl   %ecx,%r8d
	MOVL 0x70(SP)(R8*4), R9                  // 15a33 movl   0x70(%rsp,%r8,4),%r9d
	JMP L1599e                               // 15a38 jmpq   1599e <HUF_buildCTable_wksp+0x98e>

L15a3d:
	MOVL $3, R11                             // 15a3d movl   $0x3,%r11d
	MOVL DI, R10                             // 15a43 movl   %edi,%r10d
	JMP L15992                               // 15a46 jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a4b:
	MOVL $4, R11                             // 15a4b movl   $0x4,%r11d
	MOVL DI, R10                             // 15a51 movl   %edi,%r10d
	JMP L15992                               // 15a54 jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a59:
	MOVL $5, R11                             // 15a59 movl   $0x5,%r11d
	MOVL DI, R10                             // 15a5f movl   %edi,%r10d
	JMP L15992                               // 15a62 jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a67:
	MOVL $6, R11                             // 15a67 movl   $0x6,%r11d
	MOVL DI, R10                             // 15a6d movl   %edi,%r10d
	JMP L15992                               // 15a70 jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a75:
	MOVL $7, R11                             // 15a75 movl   $0x7,%r11d
	MOVL DI, R10                             // 15a7b movl   %edi,%r10d
	JMP L15992                               // 15a7e jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a83:
	MOVL $8, R11                             // 15a83 movl   $0x8,%r11d
	MOVL DI, R10                             // 15a89 movl   %edi,%r10d
	JMP L15992                               // 15a8c jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a91:
	MOVL $9, R11                             // 15a91 movl   $0x9,%r11d
	MOVL DI, R10                             // 15a97 movl   %edi,%r10d
	JMP L15992                               // 15a9a jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15a9f:
	MOVL $-0x80000000, (BP)                  // 15a9f movl   $0x80000000,0x0(%rbp)
	MOVB $0, 0x7(R8)                         // 15aa6 movb   $0x0,0x7(%r8)
	TESTL SI, SI                             // 15aab testl  %esi,%esi
	JMI L153f7                               // 15aad js     153f7 <HUF_buildCTable_wksp+0x3e7>
	JMP L153ce                               // 15ab3 jmpq   153ce <HUF_buildCTable_wksp+0x3be>
	// 15ab8 nopl   0x0(%rax,%rax,1)

L15ac0:
	MOVL $10, R11                            // 15ac0 movl   $0xa,%r11d
	MOVL DI, R10                             // 15ac6 movl   %edi,%r10d
	JMP L15992                               // 15ac9 jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15ace:
	MOVL SI, R11                             // 15ace movl   %esi,%r11d
	JMP L15250                               // 15ad1 jmpq   15250 <HUF_buildCTable_wksp+0x240>

L15ad6:
	MOVL $11, R11                            // 15ad6 movl   $0xb,%r11d
	MOVL DI, R10                             // 15adc movl   %edi,%r10d
	JMP L15992                               // 15adf jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15ae4:
	MOVL $12, R11                            // 15ae4 movl   $0xc,%r11d
	MOVL DI, R10                             // 15aea movl   %edi,%r10d
	JMP L15992                               // 15aed jmpq   15992 <HUF_buildCTable_wksp+0x982>

L15af2:
	MOVL SI, DX                              // 15af2 movl   %esi,%edx
	XORL AX, AX                              // 15af4 xorl   %eax,%eax
	JMP L15480                               // 15af6 jmpq   15480 <HUF_buildCTable_wksp+0x470>

L15afb:
	LEAL 0x1(DX), CX                         // 15afb leal   0x1(%rdx),%ecx
	JMP L15a26                               // 15afe jmpq   15a26 <HUF_buildCTable_wksp+0xa16>

L15b03:
	MOVL $1, R11                             // 15b03 movl   $0x1,%r11d
	MOVL DI, R10                             // 15b09 movl   %edi,%r10d
	JMP L15992                               // 15b0c jmpq   15992 <HUF_buildCTable_wksp+0x982>

	// 15b16 cs nopw 0x0(%rax,%rax,1)

allzero:
	MOVQ $-2, ret+24(FP)
	RET

infeasible:
	MOVQ $-3, ret+24(FP)
	RET

// hufSimpleQuickSort is gcc's HUF_simpleQuickSort (0x10a60-0x10cf8, the
// insertion sort inlined), transliterated the same way: DI = arr, SI = low,
// DX = high; BX, BP and R12-R15 preserved; clobbers the rest.
//
// gcc's code calls itself on the smaller partition. A NOSPLIT Go function
// cannot recurse (the linker rejects the cycle), so a call pushes a record
// of what C's callee keeps (BX, BP, R12-R15, C's 0x8(SP) slot, the return
// site) and jumps to the entry; C's return pops one and resumes there. The
// order of work is C's. Depth: a call sorts at most (n-1)/2 of its caller's
// n elements and recurses only at 9 or more, so from n <= 256 it is at most
// 5 records deep; there are 8.
//
// Frame: 0x08 C's slot, 0x10 record count, 0x18-0x3f BX and R12-R15 at
// entry, 0x40-0x23f the records (64 bytes each).

TEXT hufSimpleQuickSort<>(SB), NOSPLIT, $576-0
	MOVQ BX, 0x18(SP)
	MOVQ R12, 0x20(SP)
	MOVQ R13, 0x28(SP)
	MOVQ R14, 0x30(SP)
	MOVQ R15, 0x38(SP)
	MOVQ $0, 0x10(SP)
Q10a60:
	MOVL DX, R13                             // 10a66 movl   %edx,%r13d
	MOVLQSX SI, R12                          // 10a6b movslq %esi,%r12
	SUBL R12, DX                             // 10a6f subl   %r12d,%edx
	MOVQ DI, BP                              // 10a72 movq   %rdi,%rbp
	CMPL DX, $7                              // 10a7a cmpl   $0x7,%edx
	JLE Q10bd0                               // 10a7d jle    10bd0 <HUF_simpleQuickSort+0x170>
	MOVLQSX R13, R14                         // 10a83 movslq %r13d,%r14
	LEAL -0x1(R12), R15                      // 10a86 leal   -0x1(%r12),%r15d
	CMPL R13, R12                            // 10a8b cmpl   %r12d,%r13d
	JLE Q10bbf                               // 10a8e jle    10bbf <HUF_simpleQuickSort+0x15f>

Q10a94:
	LEAQ (BP)(R14*8), R11                    // 10a94 leaq   0x0(%rbp,%r14,8),%r11
	MOVL R15, BX                             // 10a99 movl   %r15d,%ebx
	MOVL (R11), CX                           // 10a9c movl   (%r11),%ecx
	CMPL R12, R13                            // 10a9f cmpl   %r13d,%r12d
	JGE Q10b57                               // 10aa2 jge    10b57 <HUF_simpleQuickSort+0xf7>

Q10aa8:
	MOVL R13, DX                             // 10aa8 movl   %r13d,%edx
	MOVLQSX R12, SI                          // 10aab movslq %r12d,%rsi
	SUBL R12, DX                             // 10aae subl   %r12d,%edx
	LEAQ (BP)(SI*8), AX                      // 10ab1 leaq   0x0(%rbp,%rsi,8),%rax
	ADDQ SI, DX                              // 10ab6 addq   %rsi,%rdx
	LEAQ (BP)(DX*8), SI                      // 10ab9 leaq   0x0(%rbp,%rdx,8),%rsi
	// 10abe xchgw  %ax,%ax

Q10ac0:
	CMPL CX, (AX)                            // 10ac0 cmpl   (%rax),%ecx
	JCC Q10aed                               // 10ac2 jae    10aed <HUF_simpleQuickSort+0x8d>
	ADDL $1, BX                              // 10ac4 addl   $0x1,%ebx
	MOVQ (AX), R10                           // 10ac7 movq   (%rax),%r10
	MOVLQSX BX, DX                           // 10aca movslq %ebx,%rdx
	LEAQ (BP)(DX*8), DX                      // 10acd leaq   0x0(%rbp,%rdx,8),%rdx
	MOVL (DX), R9                            // 10ad2 movl   (%rdx),%r9d
	MOVWLZX 0x4(DX), R8                      // 10ad5 movzwl 0x4(%rdx),%r8d
	MOVWLZX 0x6(DX), DI                      // 10ada movzwl 0x6(%rdx),%edi
	MOVQ R10, (DX)                           // 10ade movq   %r10,(%rdx)
	MOVL R9, (AX)                            // 10ae1 movl   %r9d,(%rax)
	MOVW R8, 0x4(AX)                         // 10ae4 movw   %r8w,0x4(%rax)
	MOVW DI, 0x6(AX)                         // 10ae9 movw   %di,0x6(%rax)

Q10aed:
	ADDQ $8, AX                              // 10aed addq   $0x8,%rax
	CMPQ AX, SI                              // 10af1 cmpq   %rsi,%rax
	JNE Q10ac0                               // 10af4 jne    10ac0 <HUF_simpleQuickSort+0x60>
	LEAL 0x1(BX), DX                         // 10af6 leal   0x1(%rbx),%edx
	MOVL DX, SI                              // 10af9 movl   %edx,%esi
	SUBL R12, SI                             // 10afb subl   %r12d,%esi

Q10afe:
	MOVLQSX BX, CX                           // 10afe movslq %ebx,%rcx
	MOVQ (R11), R8                           // 10b01 movq   (%r11),%r8
	LEAQ 0x8(BP)(CX*8), AX                   // 10b04 leaq   0x8(%rbp,%rcx,8),%rax
	MOVQ (AX), DI                            // 10b09 movq   (%rax),%rdi
	MOVQ R8, (AX)                            // 10b0c movq   %r8,(%rax)
	LEAL 0x2(BX), AX                         // 10b0f leal   0x2(%rbx),%eax
	MOVQ DI, (R11)                           // 10b12 movq   %rdi,(%r11)
	MOVL R13, DI                             // 10b15 movl   %r13d,%edi
	SUBL DX, DI                              // 10b18 subl   %edx,%edi
	CMPL DI, SI                              // 10b1a cmpl   %esi,%edi
	JLE Q10b60                               // 10b1c jle    10b60 <HUF_simpleQuickSort+0x100>
	MOVL BX, DX                              // 10b1e movl   %ebx,%edx
	MOVL R12, SI                             // 10b20 movl   %r12d,%esi
	MOVQ BP, DI                              // 10b23 movq   %rbp,%rdi
	MOVL AX, 0x8(SP)                         // 10b26 movl   %eax,0x8(%rsp)
	// 10b2a callq HUF_simpleQuickSort: push a record, enter again
	MOVQ 0x10(SP), R10
	CMPQ R10, $8
	JCC qsdeep
	SHLQ $6, R10
	LEAQ 0x40(SP)(R10*1), R10
	MOVQ BX, 0(R10)
	MOVQ BP, 8(R10)
	MOVQ R12, 16(R10)
	MOVQ R13, 24(R10)
	MOVQ R14, 32(R10)
	MOVQ R15, 40(R10)
	MOVQ 0x8(SP), R11
	MOVQ R11, 48(R10)
	MOVQ $0, 56(R10)
	INCQ 0x10(SP)
	JMP Q10a60

Q10b2f:
	MOVL 0x8(SP), AX                         // 10b2f movl   0x8(%rsp),%eax
	CMPL R13, AX                             // 10b33 cmpl   %eax,%r13d
	JLE Q10bbf                               // 10b36 jle    10bbf <HUF_simpleQuickSort+0x15f>
	LEAL 0x1(BX), R15                        // 10b3c leal   0x1(%rbx),%r15d
	LEAQ (BP)(R14*8), R11                    // 10b40 leaq   0x0(%rbp,%r14,8),%r11
	MOVL AX, R12                             // 10b45 movl   %eax,%r12d
	MOVL (R11), CX                           // 10b48 movl   (%r11),%ecx
	MOVL R15, BX                             // 10b4b movl   %r15d,%ebx
	CMPL R12, R13                            // 10b4e cmpl   %r13d,%r12d
	JLT Q10aa8                               // 10b51 jl     10aa8 <HUF_simpleQuickSort+0x48>

Q10b57:
	MOVL R12, DX                             // 10b57 movl   %r12d,%edx
	XORL SI, SI                              // 10b5a xorl   %esi,%esi
	JMP Q10afe                               // 10b5c jmp    10afe <HUF_simpleQuickSort+0x9e>
	// 10b5e xchgw  %ax,%ax

Q10b60:
	MOVL R13, DX                             // 10b60 movl   %r13d,%edx
	MOVL AX, SI                              // 10b63 movl   %eax,%esi
	MOVQ BP, DI                              // 10b65 movq   %rbp,%rdi
	MOVQ CX, 0x8(SP)                         // 10b68 movq   %rcx,0x8(%rsp)
	// 10b6d callq HUF_simpleQuickSort: push a record, enter again
	MOVQ 0x10(SP), R10
	CMPQ R10, $8
	JCC qsdeep
	SHLQ $6, R10
	LEAQ 0x40(SP)(R10*1), R10
	MOVQ BX, 0(R10)
	MOVQ BP, 8(R10)
	MOVQ R12, 16(R10)
	MOVQ R13, 24(R10)
	MOVQ R14, 32(R10)
	MOVQ R15, 40(R10)
	MOVQ 0x8(SP), R11
	MOVQ R11, 48(R10)
	MOVQ $1, 56(R10)
	INCQ 0x10(SP)
	JMP Q10a60

Q10b72:
	CMPL R12, BX                             // 10b72 cmpl   %ebx,%r12d
	JGE Q10bbf                               // 10b75 jge    10bbf <HUF_simpleQuickSort+0x15f>
	MOVQ 0x8(SP), R14                        // 10b77 movq   0x8(%rsp),%r14
	MOVL BX, R13                             // 10b7c movl   %ebx,%r13d
	JMP Q10a94                               // 10b7f jmpq   10a94 <HUF_simpleQuickSort+0x34>
	// 10b84 nopl   0x0(%rax)

Q10b88:
	ADDL $1, CX                              // 10b88 addl   $0x1,%ecx
	MOVLQSX CX, CX                           // 10b8b movslq %ecx,%rcx
	LEAQ (R14)(CX*8), CX                     // 10b8e leaq   (%r14,%rcx,8),%rcx

Q10b92:
	ADDL $1, SI                              // 10b92 addl   $0x1,%esi
	MOVL DX, (CX)                            // 10b95 movl   %edx,(%rcx)
	ADDL $1, DI                              // 10b97 addl   $0x1,%edi
	ADDL $1, R8                              // 10b9a addl   $0x1,%r8d
	MOVW R13, 0x4(CX)                        // 10b9e movw   %r13w,0x4(%rcx)
	ADDL $1, R10                             // 10ba3 addl   $0x1,%r10d
	ADDL $1, R11                             // 10ba7 addl   $0x1,%r11d
	ADDL $1, BX                              // 10bab addl   $0x1,%ebx
	MOVW R12, 0x6(CX)                        // 10bae movw   %r12w,0x6(%rcx)
	ADDQ $1, R9                              // 10bb3 addq   $0x1,%r9
	ADDQ $8, AX                              // 10bb7 addq   $0x8,%rax
	CMPL SI, BP                              // 10bbb cmpl   %ebp,%esi
	JNE Q10c10                               // 10bbd jne    10c10 <HUF_simpleQuickSort+0x1b0>

Q10bbf:
	// 10bcd retq: pop a record and resume its caller, or return
	MOVQ 0x10(SP), R10
	TESTQ R10, R10
	JEQ qsret
	DECQ R10
	MOVQ R10, 0x10(SP)
	SHLQ $6, R10
	LEAQ 0x40(SP)(R10*1), R10
	MOVQ 0(R10), BX
	MOVQ 8(R10), BP
	MOVQ 16(R10), R12
	MOVQ 24(R10), R13
	MOVQ 32(R10), R14
	MOVQ 40(R10), R15
	MOVQ 48(R10), R11
	MOVQ R11, 0x8(SP)
	CMPQ 56(R10), $0
	JEQ Q10b2f
	JMP Q10b72

qsret:
	MOVQ 0x18(SP), BX
	MOVQ 0x20(SP), R12
	MOVQ 0x28(SP), R13
	MOVQ 0x30(SP), R14
	MOVQ 0x38(SP), R15
	RET

qsdeep:
	MOVQ $0, 0(R10) // unreachable (see above): fault rather than overflow the records
	JMP qsdeep
	// 10bce xchgw  %ax,%ax

Q10bd0:
	LEAQ (DI)(R12*8), R14                    // 10bd0 leaq   (%rdi,%r12,8),%r14
	TESTL DX, DX                             // 10bd4 testl  %edx,%edx
	JLE Q10bbf                               // 10bd6 jle    10bbf <HUF_simpleQuickSort+0x15f>
	LEAQ -0x38(R14), AX                      // 10bd8 leaq   -0x38(%r14),%rax
	LEAL -0x1(DX), BP                        // 10bdc leal   -0x1(%rdx),%ebp
	MOVL $-6, BX                             // 10bdf movl   $0xfffffffa,%ebx
	MOVQ $-7, R9                             // 10be4 movq   $0xfffffffffffffff9,%r9
	MOVL $-5, R11                            // 10beb movl   $0xfffffffb,%r11d
	MOVL $-4, R10                            // 10bf1 movl   $0xfffffffc,%r10d
	MOVL $-3, R8                             // 10bf7 movl   $0xfffffffd,%r8d
	MOVL $-2, DI                             // 10bfd movl   $0xfffffffe,%edi
	MOVL $-1, SI                             // 10c02 movl   $0xffffffff,%esi
	// 10c07 nopw   0x0(%rax,%rax,1)

Q10c10:
	MOVL 0x40(AX), DX                        // 10c10 movl   0x40(%rax),%edx
	MOVWLZX 0x44(AX), R13                    // 10c13 movzwl 0x44(%rax),%r13d
	LEAL 0x7(R9), CX                         // 10c18 leal   0x7(%r9),%ecx
	MOVWLZX 0x46(AX), R12                    // 10c1c movzwl 0x46(%rax),%r12d
	CMPL 0x38(AX), DX                        // 10c21 cmpl   %edx,0x38(%rax)
	JCC Q10b88                               // 10c24 jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x38(AX), CX                        // 10c2a movq   0x38(%rax),%rcx
	MOVQ CX, 0x40(AX)                        // 10c2e movq   %rcx,0x40(%rax)
	MOVL SI, CX                              // 10c32 movl   %esi,%ecx
	CMPL SI, $-1                             // 10c34 cmpl   $0xffffffff,%esi
	JEQ Q10cf0                               // 10c37 je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x30(AX), DX                        // 10c3d cmpl   %edx,0x30(%rax)
	JCC Q10b88                               // 10c40 jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x30(AX), CX                        // 10c46 movq   0x30(%rax),%rcx
	MOVQ CX, 0x38(AX)                        // 10c4a movq   %rcx,0x38(%rax)
	MOVL DI, CX                              // 10c4e movl   %edi,%ecx
	CMPL DI, $-1                             // 10c50 cmpl   $0xffffffff,%edi
	JEQ Q10cf0                               // 10c53 je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x28(AX), DX                        // 10c59 cmpl   %edx,0x28(%rax)
	JCC Q10b88                               // 10c5c jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x28(AX), CX                        // 10c62 movq   0x28(%rax),%rcx
	MOVQ CX, 0x30(AX)                        // 10c66 movq   %rcx,0x30(%rax)
	MOVL R8, CX                              // 10c6a movl   %r8d,%ecx
	CMPL R8, $-1                             // 10c6d cmpl   $0xffffffff,%r8d
	JEQ Q10cf0                               // 10c71 je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x20(AX), DX                        // 10c73 cmpl   %edx,0x20(%rax)
	JCC Q10b88                               // 10c76 jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x20(AX), CX                        // 10c7c movq   0x20(%rax),%rcx
	MOVQ CX, 0x28(AX)                        // 10c80 movq   %rcx,0x28(%rax)
	MOVL R10, CX                             // 10c84 movl   %r10d,%ecx
	CMPL R10, $-1                            // 10c87 cmpl   $0xffffffff,%r10d
	JEQ Q10cf0                               // 10c8b je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x18(AX), DX                        // 10c8d cmpl   %edx,0x18(%rax)
	JCC Q10b88                               // 10c90 jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x18(AX), CX                        // 10c96 movq   0x18(%rax),%rcx
	MOVQ CX, 0x20(AX)                        // 10c9a movq   %rcx,0x20(%rax)
	MOVL R11, CX                             // 10c9e movl   %r11d,%ecx
	CMPL R11, $-1                            // 10ca1 cmpl   $0xffffffff,%r11d
	JEQ Q10cf0                               // 10ca5 je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x10(AX), DX                        // 10ca7 cmpl   %edx,0x10(%rax)
	JCC Q10b88                               // 10caa jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x10(AX), CX                        // 10cb0 movq   0x10(%rax),%rcx
	MOVQ CX, 0x18(AX)                        // 10cb4 movq   %rcx,0x18(%rax)
	MOVL BX, CX                              // 10cb8 movl   %ebx,%ecx
	CMPL BX, $-1                             // 10cba cmpl   $0xffffffff,%ebx
	JEQ Q10cf0                               // 10cbd je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL 0x8(AX), DX                         // 10cbf cmpl   %edx,0x8(%rax)
	JCC Q10b88                               // 10cc2 jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ 0x8(AX), CX                         // 10cc8 movq   0x8(%rax),%rcx
	MOVQ CX, 0x10(AX)                        // 10ccc movq   %rcx,0x10(%rax)
	MOVL R9, CX                              // 10cd0 movl   %r9d,%ecx
	CMPL R9, $-1                             // 10cd3 cmpl   $0xffffffff,%r9d
	JEQ Q10cf0                               // 10cd7 je     10cf0 <HUF_simpleQuickSort+0x290>
	CMPL (AX), DX                            // 10cd9 cmpl   %edx,(%rax)
	JCC Q10b88                               // 10cdb jae    10b88 <HUF_simpleQuickSort+0x128>
	MOVQ (AX), AX                            // 10ce1 movq   (%rax),%rax
	MOVQ AX, 0x8(R14)(R9*8)                  // 10ce4 movq   %rax,0x8(%r14,%r9,8)
	// 10ce9 nopl   0x0(%rax)

Q10cf0:
	MOVQ R14, CX                             // 10cf0 movq   %r14,%rcx
	JMP Q10b92                               // 10cf3 jmpq   10b92 <HUF_simpleQuickSort+0x132>

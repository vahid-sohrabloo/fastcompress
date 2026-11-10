# Builds ldref and ldbench straight from ref/libdeflate/lib sources (libdeflate
# v1.26, no system libdeflate). Runtime BMI2/AVX2/PCLMUL dispatch is compiled in
# by default: libdeflate marks the variants with per-function
# __attribute__((target(...))) (see lib/x86), so no -m flags are needed and the
# binaries run on any x86-64 CPU (exactly as the shipped library does).
#
# ONE translation unit: deflate_compress.c, deflate_decompress.c, gzip_*.c and
# zlib_*.c are #included by the driver (the lib has no duplicate statics among
# them except the BITBUF_NBITS macro, handled in ldref.c), so later dump-* subcommands can reach static internals. utils.c,
# crc32.c, adler32.c and the arch cpu_features.c are separate objects (they
# define the externs the TU uses; including them too would be harmless but
# crc32.c/adler32.c pull in many arch templates).
LD   := ../libdeflate
ARCH := $(shell uname -m)
ifneq ($(filter x86_64 amd64,$(ARCH)),)
ARCHSRC := $(LD)/lib/x86/cpu_features.c
else ifneq ($(filter aarch64 arm64,$(ARCH)),)
ARCHSRC := $(LD)/lib/arm/cpu_features.c
else
ARCHSRC :=
endif
SRCS := $(LD)/lib/utils.c $(LD)/lib/crc32.c $(LD)/lib/adler32.c $(ARCHSRC)
HDRS := $(wildcard $(LD)/lib/*.h $(LD)/lib/*/*.h) $(LD)/libdeflate.h $(LD)/common_defs.h
CFLAGS ?= -O2
override CFLAGS += -I$(LD) -I$(LD)/lib

all: ldref ldbench

ldref: ldref.c $(SRCS) $(HDRS) $(wildcard $(LD)/lib/*.c)
	$(CC) $(CFLAGS) -o $@ ldref.c $(SRCS)

ldbench: ldbench.c $(SRCS) $(HDRS) $(wildcard $(LD)/lib/*.c)
	$(CC) $(CFLAGS) -o $@ ldbench.c $(LD)/lib/deflate_compress.c $(LD)/lib/deflate_decompress.c \
		$(LD)/lib/gzip_compress.c $(LD)/lib/gzip_decompress.c $(LD)/lib/zlib_compress.c \
		$(LD)/lib/zlib_decompress.c $(SRCS)

clean:
	rm -f ldref ldbench
.PHONY: all clean

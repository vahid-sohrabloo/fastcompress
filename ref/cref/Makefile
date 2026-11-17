# Builds cref straight from ref/zstd/lib sources (no system libzstd), single-threaded
# (ZSTD_MULTITHREAD is deliberately NOT defined: the lib tests it with #ifdef).
# lib/compress/zstd_compress.c is compiled from a patched COPY in build/ (seqdump.patch
# adds a no-op-by-default seqStore dump hook); ref/zstd itself is never modified.
ZLIB := ../zstd/lib
ALL  := $(wildcard $(ZLIB)/common/*.c) $(wildcard $(ZLIB)/compress/*.c) $(wildcard $(ZLIB)/decompress/*.c) \
        $(wildcard $(ZLIB)/dictBuilder/*.c)
SRCS := $(filter-out $(ZLIB)/compress/zstd_compress.c,$(ALL))
CFLAGS ?= -O2
override CFLAGS += -DZSTD_STATIC_LINKING_ONLY -DFSE_STATIC_LINKING_ONLY -DHUF_STATIC_LINKING_ONLY \
          -DZSTD_DISABLE_ASM -DZSTD_LEGACY_SUPPORT=0 -I$(ZLIB) -I$(ZLIB)/common -I$(ZLIB)/compress -I$(ZLIB)/dictBuilder

cref: cref.c build/zstd_compress.c $(SRCS)
	$(CC) $(CFLAGS) -o $@ cref.c build/zstd_compress.c $(SRCS)

build/zstd_compress.c: $(ZLIB)/compress/zstd_compress.c seqdump.patch
	mkdir -p build
	cp $(ZLIB)/compress/zstd_compress.c $@
	patch $@ < seqdump.patch

clean:
	rm -rf cref build
.PHONY: clean

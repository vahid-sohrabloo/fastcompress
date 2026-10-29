# Builds cref straight from ref/zstd/lib sources (no system libzstd), single-threaded.
ZLIB := ../zstd/lib
SRCS := $(wildcard $(ZLIB)/common/*.c) $(wildcard $(ZLIB)/compress/*.c) $(wildcard $(ZLIB)/decompress/*.c)
CFLAGS ?= -O2
CFLAGS += -DZSTD_STATIC_LINKING_ONLY -DFSE_STATIC_LINKING_ONLY -DHUF_STATIC_LINKING_ONLY \
          -DZSTD_MULTITHREAD=0 -DZSTD_DISABLE_ASM \
          -I$(ZLIB) -I$(ZLIB)/common -I$(ZLIB)/compress

cref: cref.c $(SRCS)
	$(CC) $(CFLAGS) -o $@ cref.c $(SRCS)

clean:
	rm -f cref
.PHONY: clean

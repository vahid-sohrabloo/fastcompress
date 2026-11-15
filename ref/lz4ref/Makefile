# Builds lz4ref straight from ref/lz4/lib sources (lz4 v1.11.1, no system liblz4),
# single-threaded (LZ4F_MULTITHREAD off), -O2 like cref.
LZ   := ../lz4/lib
SRCS := $(LZ)/lz4.c $(LZ)/lz4hc.c $(LZ)/lz4frame.c $(LZ)/xxhash.c
CFLAGS ?= -O2
override CFLAGS += -I$(LZ) -DLZ4F_MULTITHREAD=0

lz4ref: lz4ref.c $(SRCS) $(wildcard $(LZ)/*.h)
	$(CC) $(CFLAGS) -o $@ lz4ref.c $(SRCS)

clean:
	rm -f lz4ref
.PHONY: clean

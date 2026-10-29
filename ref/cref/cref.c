/*
 * cref - C reference driver for fastzstd. Built straight from ref/zstd/lib
 * (libzstd v1.5.7, single-threaded). Reads stdin, writes stdout. Exit status 0
 * on success, nonzero (with a message on stderr) on any error.
 *
 * Common options (accepted by compress, stream, seqs where meaningful):
 *   -level L        compression level (default 3)
 *   -checksum [0|1] content checksum flag (default 0; bare -checksum means 1)
 *   -pledged 0|1    1 (default): content size is known to the compressor.
 *                   0: content size is UNKNOWN (ZSTD_CONTENTSIZE_UNKNOWN); the
 *                   frame header carries no content size.
 *   -window-log W   ZSTD_c_windowLog (default 0 = level default)
 *   -dict FILE      dictionary file (ZSTD_dct_auto unless -raw-dict 1)
 *   -raw-dict 0|1   1: load the dictionary as ZSTD_dct_rawContent
 *   -lit-mode M     ZSTD_c_literalCompressionMode: 0 auto (default),
 *                   1 ZSTD_ps_enable, 2 ZSTD_ps_disable
 *
 * MODES
 *
 * compress [common opts]
 *   -pledged 1: ZSTD_compress2 (it pledges the exact input size).
 *   -pledged 0: NOTE ZSTD_compressStream2(ZSTD_e_end) as the very first call
 *     auto-pledges inSize in 1.5.7 (ZSTD_CCtx_init_compressStream2), which
 *     would make the size known. To really leave it unknown, we first issue
 *     one ZSTD_e_continue call with an EMPTY input (this runs the init stage
 *     with pledged size unknown), then one ZSTD_e_end call carrying the whole
 *     input, looping until it returns 0.
 *   Output: the zstd frame (raw bytes).
 *
 * stream [common opts, -pledged default 0] -chunks N1,N2,... -flush-after i,j,...
 *   Feeds the input in chunks of the given sizes (must sum to the input length;
 *   a 0 size means an empty ZSTD_e_continue call) with ZSTD_e_continue; after
 *   chunk index k (0-based) listed in -flush-after, loops ZSTD_e_flush (empty
 *   input) until it returns 0; finally loops ZSTD_e_end until 0.
 *   -pledged 1 calls ZSTD_CCtx_setPledgedSrcSize(total input length) first.
 *   Output: the zstd frame (raw bytes).
 *
 * xxh64 [-seed S]      S decimal or 0x-hex. Output: 16 lowercase hex digits + "\n".
 *
 * hist                 HIST_count over stdin with maxSymbolValue=255 on entry.
 *   Output, two text lines:
 *     <c0> <c1> ... <c255>\n          (256 space-separated decimal counts)
 *     maxSymbol=<n> largest=<n>\n     (HIST_count's *maxSymbolValuePtr on exit
 *                                      and its return value)
 *
 * fse [-tablelog T]    T = maximum tableLog (0 or absent: FSE_DEFAULT_TABLELOG).
 *   FSE_compress2/FSE_compress_wksp does not exist in the 1.5.7 lib, so this
 *   re-assembles exactly what the old FSE_compress_wksp did, from lib pieces:
 *     rc = 0 (-> empty output) if srcSize <= 1
 *     HIST_count_wksp(count, maxSymbol=255, src) -> maxCount
 *     maxCount == srcSize        -> rc = 1 (RLE)
 *     maxCount == 1              -> rc = 0
 *     maxCount < (srcSize >> 7)  -> rc = 0
 *     tableLog = FSE_optimalTableLog(T, srcSize, maxSymbol)
 *     FSE_normalizeCount(norm, tableLog, count, srcSize, maxSymbol, useLowProbCount = srcSize >= 2048)
 *     nc = FSE_writeNCount(norm, maxSymbol, tableLog)
 *     FSE_buildCTable_wksp; c = FSE_compress_usingCTable(src)
 *     c == 0 -> rc = 0; if nc + c >= srcSize - 1 -> rc = 0; else rc = nc + c
 *   Output (raw bytes): rc = 0 -> EMPTY output; RLE -> exactly 1 byte, the
 *   repeated symbol; otherwise NCount header || FSE payload (>= 2 bytes).
 *
 * huf1x | huf4x [-tablelog T] [-flags F]
 *   HUF_compress1X_repeat / HUF_compress4X_repeat with maxSymbolValue=255,
 *   tableLog T (0 or absent: 0, which HUF treats as HUF_TABLELOG_DEFAULT),
 *   a HUF_WORKSPACE_SIZE workspace, a fresh table, repeat=HUF_repeat_none,
 *   flags F (default 0; bit values are HUF_flags_e: 1 bmi2, 2 optimalDepth,
 *   4 preferRepeat, 8 suspectUncompressible, 16 disableAsm, 32 disableFast).
 *   Output (raw bytes): the first rc bytes of dst. rc = 0 -> EMPTY output (not
 *   compressible; also srcSize <= 1); rc = 1 -> exactly 1 byte, the repeated
 *   symbol (RLE); else the HUF table header || stream(s).
 *
 * seqs [common opts] [-frame-out FILE]
 *   Runs a NORMAL ZSTD_compress2 (pledged size known, identical output to
 *   `compress`; the frame is written to FILE if -frame-out is given) and dumps
 *   the REAL seqStore of every emitted block. lib/compress/zstd_compress.c is
 *   built from a patched copy (ref/cref/seqdump.patch) that calls a hook (a
 *   no-op unless set) in ZSTD_compressBlock_internal right after the seqStore
 *   is built and the block type is known, and in
 *   ZSTD_compressSeqStore_singleBlock (the post-block-splitter path; the
 *   splitter is only enabled for strategy >= btopt, i.e. NOT reachable at
 *   levels <= 11 unless forced, but is hooked for completeness).
 *   Stdout is text, one block after another:
 *     block type=<raw|compressed|rle> src=<srcSize>\n
 *     seq lit=<ll> ml=<ml> offbase=<ob>\n     one per stored sequence, in order
 *     longlen type=<0|1|2> pos=<n>\n          seqStore.longLengthType (0 none,
 *                                             1 literalLength, 2 matchLength)
 *                                             and longLengthPos
 *     lits=<hex>\n                            seqStore literal buffer (all
 *                                             sequence literals then the trailing
 *                                             last literals); "lits=" if empty
 *   <ll> is the stored SeqDef.litLength (U16, WITHOUT the +0x10000 long-length
 *   correction), <ml> is SeqDef.mlBase + MINMATCH(3) (also without correction),
 *   <ob> is SeqDef.offBase (1..3 repcode, else offset + 3). The effective
 *   length of the sequence at index == longlen.pos is stored + 0x10000 for the
 *   field named by longlen.type. A block with no seqStore (ZSTD_buildSeqStore
 *   declined a tiny block; it is emitted raw) prints no seq lines, "longlen
 *   type=0 pos=0", and "lits=". type is the block's final form (raw =
 *   uncompressed, rle, or compressed) and src the bytes it covers.
 *
 * presplit -level N    N in 0..4 (the ZSTD_splitBlock level, NOT a compression
 *   level; -level is reused as the option name). Calls ZSTD_splitBlock on the
 *   first 128 KiB of stdin (input must be at least 128 KiB, else exit 1).
 *   Output: the returned split position as a decimal line, e.g. "65536\n".
 *
 * OUTPUT BUFFER CONTRACT: every ZSTD_compressStream2 call in cref (compress
 * -pledged 0, and stream) uses an output buffer of exactly
 * ZSTD_CStreamOutSize() bytes which is drained into the result after each
 * call. (Output-buffer free space can change the bytes, via the "compress
 * directly into output" shortcut in ZSTD_compressStream_generic.)
 *
 * NOTE on fse: it is an oracle for the individual FSE functions only
 * (HIST_count, FSE_optimalTableLog, FSE_normalizeCount, FSE_writeNCount,
 * FSE_buildCTable_wksp, FSE_compress_usingCTable). Its outer heuristics come
 * from the removed/deprecated FSE_compress, not from the compressor's own
 * ZSTD_buildCTable / HUF_compressWeights paths.
 */
#define _POSIX_C_SOURCE 200809L
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include "zstd.h"
#include "zstd_errors.h"
#include "xxhash.h"
#include "fse.h"
#include "huf.h"
#include "hist.h"
#include "zstd_compress_internal.h"
#include "zstd_preSplit.h"

extern void (*ZSTD_crefHook)(const SeqStore_t*, size_t, int); /* seqdump.patch */

static void die(const char* msg) { fprintf(stderr, "cref: %s\n", msg); exit(1); }
static void dieZ(const char* what, size_t rc) {
    fprintf(stderr, "cref: %s: %s\n", what, ZSTD_getErrorName(rc)); exit(1);
}

typedef struct {
    int level, checksum, pledged, windowLog, rawDict, tablelog, flags, litMode;
    const char* dictFile;
    unsigned long long seed;
    const char* chunks;
    const char* flushAfter;
    const char* frameOut;
} Opts;

static void* readAll(FILE* f, size_t* n) {
    size_t cap = 1 << 20, len = 0;
    unsigned char* b = malloc(cap);
    if (!b) die("oom");
    for (;;) {
        size_t r = fread(b + len, 1, cap - len, f);
        len += r;
        if (len < cap) break;
        cap *= 2;
        b = realloc(b, cap);
        if (!b) die("oom");
    }
    if (ferror(f)) die("read error");
    *n = len;
    return b;
}

static void writeAll(const void* p, size_t n) {
    if (n && fwrite(p, 1, n, stdout) != n) die("write error");
}

static void parseOpts(int argc, char** argv, Opts* o, int defaultPledged) {
    memset(o, 0, sizeof(*o));
    o->level = 3; o->pledged = defaultPledged;
    for (int i = 2; i < argc; i++) {
        const char* a = argv[i];
        const char* v = (i + 1 < argc) ? argv[i + 1] : NULL;
        if (!strcmp(a, "-checksum")) {
            if (v && (!strcmp(v, "0") || !strcmp(v, "1"))) { o->checksum = atoi(v); i++; }
            else o->checksum = 1;
            continue;
        }
        if (!v) { fprintf(stderr, "cref: option %s needs a value\n", a); exit(2); }
        i++;
        if      (!strcmp(a, "-level"))      o->level = atoi(v);
        else if (!strcmp(a, "-pledged"))    o->pledged = atoi(v);
        else if (!strcmp(a, "-window-log")) o->windowLog = atoi(v);
        else if (!strcmp(a, "-dict"))       o->dictFile = v;
        else if (!strcmp(a, "-raw-dict"))   o->rawDict = atoi(v);
        else if (!strcmp(a, "-lit-mode"))   o->litMode = atoi(v);
        else if (!strcmp(a, "-tablelog"))   o->tablelog = atoi(v);
        else if (!strcmp(a, "-flags"))      o->flags = atoi(v);
        else if (!strcmp(a, "-seed"))       o->seed = strtoull(v, NULL, 0);
        else if (!strcmp(a, "-chunks"))     o->chunks = v;
        else if (!strcmp(a, "-flush-after")) o->flushAfter = v;
        else if (!strcmp(a, "-frame-out"))  o->frameOut = v;
    else { fprintf(stderr, "cref: unknown option %s\n", a); exit(2); }
    }
}

static ZSTD_CCtx* newCCtx(const Opts* o) {
    ZSTD_CCtx* c = ZSTD_createCCtx();
    if (!c) die("ZSTD_createCCtx");
    size_t r;
    r = ZSTD_CCtx_setParameter(c, ZSTD_c_compressionLevel, o->level);
    if (ZSTD_isError(r)) dieZ("set level", r);
    r = ZSTD_CCtx_setParameter(c, ZSTD_c_checksumFlag, o->checksum);
    if (ZSTD_isError(r)) dieZ("set checksum", r);
    if (o->windowLog) {
        r = ZSTD_CCtx_setParameter(c, ZSTD_c_windowLog, o->windowLog);
        if (ZSTD_isError(r)) dieZ("set windowLog", r);
    }
    if (o->litMode) {
        r = ZSTD_CCtx_setParameter(c, ZSTD_c_literalCompressionMode, o->litMode);
        if (ZSTD_isError(r)) dieZ("set literalCompressionMode", r);
    }
    if (o->dictFile) {
        FILE* f = fopen(o->dictFile, "rb");
        if (!f) die("cannot open dict file");
        size_t dn; void* d = readAll(f, &dn); fclose(f);
        r = ZSTD_CCtx_loadDictionary_advanced(c, d, dn, ZSTD_dlm_byCopy,
                o->rawDict ? ZSTD_dct_rawContent : ZSTD_dct_auto);
        if (ZSTD_isError(r)) dieZ("load dictionary", r);
        free(d);
    }
    return c;
}

/* Run compressStream2 with `endOp` over [in, in+n) until fully consumed and
 * (for flush/end) until it returns 0; appends all output to *out. Every call
 * gets an output buffer of exactly ZSTD_CStreamOutSize() bytes, drained after
 * the call (see OUTPUT BUFFER CONTRACT above). */
static void feed(ZSTD_CCtx* c, ZSTD_EndDirective op, const void* in, size_t n,
                 unsigned char** out, size_t* olen, size_t* ocap) {
    static unsigned char* buf;
    const size_t bsz = ZSTD_CStreamOutSize();
    ZSTD_inBuffer ib = { in, n, 0 };
    if (!buf && !(buf = malloc(bsz))) die("oom");
    for (;;) {
        ZSTD_outBuffer ob = { buf, bsz, 0 };
        size_t r = ZSTD_compressStream2(c, &ob, &ib, op);
        if (ZSTD_isError(r)) dieZ("ZSTD_compressStream2", r);
        if (*ocap - *olen < ob.pos) {
            *ocap = *ocap * 2 + ob.pos;
            *out = realloc(*out, *ocap);
            if (!*out) die("oom");
        }
        memcpy(*out + *olen, buf, ob.pos);
        *olen += ob.pos;
        if (op == ZSTD_e_continue) { if (ib.pos == ib.size) break; }
        else if (r == 0) break;
    }
}

static int cmdCompress(const Opts* o, const unsigned char* src, size_t n) {
    ZSTD_CCtx* c = newCCtx(o);
    unsigned char* out = NULL; size_t olen = 0, ocap = 0;
    if (o->pledged) {
        size_t cap = ZSTD_compressBound(n);
        out = malloc(cap ? cap : 1);
        if (!out) die("oom");
        size_t r = ZSTD_compress2(c, out, cap, src, n);
        if (ZSTD_isError(r)) dieZ("ZSTD_compress2", r);
        olen = r;
    } else {
        feed(c, ZSTD_e_continue, src, 0, &out, &olen, &ocap);
        feed(c, ZSTD_e_end, src, n, &out, &olen, &ocap);
    }
    writeAll(out, olen);
    return 0;
}

static int inList(const char* list, size_t idx) {
    char* s = list ? strdup(list) : NULL;
    char* save = NULL;
    if (!s) return 0;
    for (char* t = strtok_r(s, ",", &save); t; t = strtok_r(NULL, ",", &save))
        if ((size_t)strtoull(t, NULL, 10) == idx) { free(s); return 1; }
    free(s);
    return 0;
}

static int cmdStream(const Opts* o, const unsigned char* src, size_t n) {
    if (!o->chunks) die("stream needs -chunks");
    ZSTD_CCtx* c = newCCtx(o);
    if (o->pledged) {
        size_t r = ZSTD_CCtx_setPledgedSrcSize(c, n);
        if (ZSTD_isError(r)) dieZ("setPledgedSrcSize", r);
    }
    unsigned char* out = NULL; size_t olen = 0, ocap = 0;
    char* cs = strdup(o->chunks);
    size_t pos = 0, idx = 0;
    char* save = NULL;
    for (char* t = strtok_r(cs, ",", &save); t; t = strtok_r(NULL, ",", &save), idx++) {
        size_t sz = (size_t)strtoull(t, NULL, 10);
        if (sz > n - pos) die("chunks exceed input length");
        feed(c, ZSTD_e_continue, src + pos, sz, &out, &olen, &ocap);
        pos += sz;
        if (inList(o->flushAfter, idx))
            feed(c, ZSTD_e_flush, src + pos, 0, &out, &olen, &ocap);
    }
    if (pos != n) die("chunks do not cover input length");
    feed(c, ZSTD_e_end, src + pos, 0, &out, &olen, &ocap);
    writeAll(out, olen);
    return 0;
}

static int cmdXxh64(const Opts* o, const unsigned char* src, size_t n) {
    printf("%016llx\n", (unsigned long long)XXH64(src, n, o->seed));
    return 0;
}

static int cmdHist(const unsigned char* src, size_t n) {
    unsigned count[256];
    unsigned maxSym = 255;
    size_t largest = HIST_count(count, &maxSym, src, n);
    if (HIST_isError(largest)) die("HIST_count");
    for (int i = 0; i < 256; i++) printf(i ? " %u" : "%u", count[i]);
    printf("\nmaxSymbol=%u largest=%zu\n", maxSym, largest);
    return 0;
}

static int cmdFse(const Opts* o, const unsigned char* src, size_t n) {
    unsigned maxSymbol = 255;
    unsigned tableLog = o->tablelog ? (unsigned)o->tablelog : FSE_DEFAULT_TABLELOG;
    if (n <= 1) return 0;
    size_t cap = n + (n >> 7) + 1024;
    unsigned char* dst = malloc(cap);
    unsigned count[256];
    short norm[256];
    static unsigned wksp[HIST_WKSP_SIZE_U32 > FSE_BUILD_CTABLE_WORKSPACE_SIZE_U32(255, FSE_MAX_TABLELOG)
                         ? HIST_WKSP_SIZE_U32 : FSE_BUILD_CTABLE_WORKSPACE_SIZE_U32(255, FSE_MAX_TABLELOG)];
    static FSE_CTable ct[FSE_CTABLE_SIZE_U32(FSE_MAX_TABLELOG, 255)];
    size_t maxCount = HIST_count_wksp(count, &maxSymbol, src, n, wksp, sizeof(wksp));
    if (HIST_isError(maxCount)) die("HIST_count_wksp");
    if (maxCount == n) { dst[0] = src[0]; writeAll(dst, 1); return 0; }
    if (maxCount == 1) return 0;
    if (maxCount < (n >> 7)) return 0;
    tableLog = FSE_optimalTableLog(tableLog, n, maxSymbol);
    size_t r = FSE_normalizeCount(norm, tableLog, count, n, maxSymbol, n >= 2048);
    if (FSE_isError(r)) die("FSE_normalizeCount");
    size_t nc = FSE_writeNCount(dst, cap, norm, maxSymbol, tableLog);
    if (FSE_isError(nc)) die("FSE_writeNCount");
    r = FSE_buildCTable_wksp(ct, norm, maxSymbol, tableLog, wksp, sizeof(wksp));
    if (FSE_isError(r)) die("FSE_buildCTable_wksp");
    size_t cs = FSE_compress_usingCTable(dst + nc, cap - nc, src, n, ct);
    if (FSE_isError(cs)) die("FSE_compress_usingCTable");
    if (cs == 0) return 0;
    if (nc + cs >= n - 1) return 0;
    writeAll(dst, nc + cs);
    return 0;
}

static int cmdHuf(const Opts* o, const unsigned char* src, size_t n, int streams) {
    size_t cap = n + (n >> 8) + 1024;
    unsigned char* dst = malloc(cap);
    static U64 wksp[HUF_WORKSPACE_SIZE_U64];
    HUF_CElt table[HUF_CTABLE_SIZE_ST(255)];
    HUF_repeat rep = HUF_repeat_none;
    memset(table, 0, sizeof(table));
    size_t r = streams == 1
        ? HUF_compress1X_repeat(dst, cap, src, n, 255, (unsigned)o->tablelog, wksp, sizeof(wksp), table, &rep, o->flags)
        : HUF_compress4X_repeat(dst, cap, src, n, 255, (unsigned)o->tablelog, wksp, sizeof(wksp), table, &rep, o->flags);
    if (HUF_isError(r)) die("HUF_compress");
    writeAll(dst, r);
    return 0;
}

static void seqHook(const SeqStore_t* st, size_t srcSize, int type) {
    static const char hex[] = "0123456789abcdef";
    static const char* const names[] = { "raw", "compressed", "rle" };
    printf("block type=%s src=%zu\n", names[type], srcSize);
    if (!st) { printf("longlen type=0 pos=0\nlits=\n"); return; }
    for (const SeqDef* q = st->sequencesStart; q < st->sequences; q++)
        printf("seq lit=%u ml=%u offbase=%u\n", (unsigned)q->litLength,
               (unsigned)q->mlBase + 3, (unsigned)q->offBase);
    printf("longlen type=%d pos=%u\n", (int)st->longLengthType, (unsigned)st->longLengthPos);
    fputs("lits=", stdout);
    for (const BYTE* p = st->litStart; p < st->lit; p++) { putchar(hex[*p >> 4]); putchar(hex[*p & 15]); }
    putchar('\n');
}

static int cmdSeqs(const Opts* o, const unsigned char* src, size_t n) {
    ZSTD_CCtx* c = newCCtx(o);
    size_t cap = ZSTD_compressBound(n);
    unsigned char* dst = malloc(cap ? cap : 1);
    if (!dst) die("oom");
    if (n == 0) seqHook(NULL, 0, 0); /* empty input: lib writes the empty last block without a hook */
    ZSTD_crefHook = seqHook;
    size_t r = ZSTD_compress2(c, dst, cap, src, n);
    ZSTD_crefHook = NULL;
    if (ZSTD_isError(r)) dieZ("ZSTD_compress2", r);
    if (o->frameOut) {
        FILE* f = fopen(o->frameOut, "wb");
        if (!f || fwrite(dst, 1, r, f) != r || fclose(f)) die("cannot write -frame-out");
    }
    return 0;
}

static int cmdPresplit(const Opts* o, const unsigned char* src, size_t n) {
    static U64 wksp[ZSTD_SLIPBLOCK_WORKSPACESIZE / 8 + 1];
    if (n < (128 << 10) || o->level < 0 || o->level > 4) die("presplit: need >= 128 KiB input and -level 0..4");
    printf("%zu\n", ZSTD_splitBlock(src, 128 << 10, o->level, wksp, sizeof(wksp)));
    return 0;
}

int main(int argc, char** argv) {
    if (argc < 2) { fprintf(stderr, "usage: cref <mode> [options] < in > out\n"); return 2; }
    const char* m = argv[1];
    Opts o;
    size_t n;
    int rc;
    parseOpts(argc, argv, &o, !strcmp(m, "stream") ? 0 : 1);
    unsigned char* src = readAll(stdin, &n);
    if (!strcmp(m, "compress"))      rc = cmdCompress(&o, src, n);
    else if (!strcmp(m, "stream"))   rc = cmdStream(&o, src, n);
    else if (!strcmp(m, "xxh64"))    rc = cmdXxh64(&o, src, n);
    else if (!strcmp(m, "hist"))     rc = cmdHist(src, n);
    else if (!strcmp(m, "fse"))      rc = cmdFse(&o, src, n);
    else if (!strcmp(m, "huf1x"))    rc = cmdHuf(&o, src, n, 1);
    else if (!strcmp(m, "huf4x"))    rc = cmdHuf(&o, src, n, 4);
    else if (!strcmp(m, "seqs"))     rc = cmdSeqs(&o, src, n);
    else if (!strcmp(m, "presplit")) rc = cmdPresplit(&o, src, n);
    else { fprintf(stderr, "cref: unknown mode %s\n", m); return 2; }
    if (fflush(stdout)) die("write error");
    return rc;
}

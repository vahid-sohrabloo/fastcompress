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
 * seqs [common opts]
 *   ZSTD_generateSequences (deprecated, in 1.5.7 always emits a block
 *   delimiter after each block; ZSTD_c_blockDelimiters does not apply to it).
 *   Text output, one record per line:
 *     block\n                                   starts a block
 *     seq lit=<ll> ml=<ml> off=<offset> rep=<rep> offbase=<ob>\n
 *     last lit=<n>\n                            C's block delimiter (offset 0,
 *                                               matchLength 0, litLength n =
 *                                               trailing literals of the block)
 *     lits=<hex>\n                              the block's literals, in order
 *                                               (all seq literals then the trailing
 *                                               literals), reconstructed from the
 *                                               source by walking ll/ml. Empty
 *                                               block literals => "lits=".
 *   <offset> is the raw offset from ZSTD_Sequence.offset (rep-resolved);
 *   <rep> is ZSTD_Sequence.rep (0 = not a repcode, else 1..3);
 *   <ob> is seqStore's offBase: rep if rep > 0, else offset + 3 (ZSTD_REP_NUM).
 *   Per block the order is: block, seq*, last, lits.
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

static void die(const char* msg) { fprintf(stderr, "cref: %s\n", msg); exit(1); }
static void dieZ(const char* what, size_t rc) {
    fprintf(stderr, "cref: %s: %s\n", what, ZSTD_getErrorName(rc)); exit(1);
}

typedef struct {
    int level, checksum, pledged, windowLog, rawDict, tablelog, flags;
    const char* dictFile;
    unsigned long long seed;
    const char* chunks;
    const char* flushAfter;
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
        else if (!strcmp(a, "-tablelog"))   o->tablelog = atoi(v);
        else if (!strcmp(a, "-flags"))      o->flags = atoi(v);
        else if (!strcmp(a, "-seed"))       o->seed = strtoull(v, NULL, 0);
        else if (!strcmp(a, "-chunks"))     o->chunks = v;
        else if (!strcmp(a, "-flush-after")) o->flushAfter = v;
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
 * (for flush/end) until it returns 0; appends all output to *out. */
static void feed(ZSTD_CCtx* c, ZSTD_EndDirective op, const void* in, size_t n,
                 unsigned char** out, size_t* olen, size_t* ocap) {
    ZSTD_inBuffer ib = { in, n, 0 };
    for (;;) {
        if (*ocap - *olen < ZSTD_CStreamOutSize()) {
            *ocap = *ocap * 2 + ZSTD_CStreamOutSize();
            *out = realloc(*out, *ocap);
            if (!*out) die("oom");
        }
        ZSTD_outBuffer ob = { *out, *ocap, *olen };
        size_t r = ZSTD_compressStream2(c, &ob, &ib, op);
        if (ZSTD_isError(r)) dieZ("ZSTD_compressStream2", r);
        *olen = ob.pos;
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

static int cmdSeqs(const Opts* o, const unsigned char* src, size_t n) {
    ZSTD_CCtx* c = newCCtx(o);
    size_t cap = ZSTD_sequenceBound(n);
    ZSTD_Sequence* s = malloc((cap ? cap : 1) * sizeof(*s));
    if (!s) die("oom");
    size_t cnt = ZSTD_generateSequences(c, s, cap, src, n);
    if (ZSTD_isError(cnt)) dieZ("ZSTD_generateSequences", cnt);
    size_t pos = 0;
    int open = 0;
    static const char hex[] = "0123456789abcdef";
    /* Each block's literals: gather runs [pos, pos+ll) while walking. */
    unsigned char* lits = malloc(n + 1);
    size_t nl = 0;
    for (size_t i = 0; i < cnt; i++) {
        if (!open) { printf("block\n"); open = 1; nl = 0; }
        if (s[i].offset == 0 && s[i].matchLength == 0) {
            printf("last lit=%u\n", s[i].litLength);
            if (s[i].litLength > n - pos) die("literal overrun");
            memcpy(lits + nl, src + pos, s[i].litLength);
            nl += s[i].litLength; pos += s[i].litLength;
            fputs("lits=", stdout);
            for (size_t k = 0; k < nl; k++) { putchar(hex[lits[k] >> 4]); putchar(hex[lits[k] & 15]); }
            putchar('\n');
            open = 0;
        } else {
            unsigned ob = s[i].rep ? s[i].rep : s[i].offset + 3;
            printf("seq lit=%u ml=%u off=%u rep=%u offbase=%u\n",
                   s[i].litLength, s[i].matchLength, s[i].offset, s[i].rep, ob);
            if ((size_t)s[i].litLength + s[i].matchLength > n - pos) die("sequence overrun");
            memcpy(lits + nl, src + pos, s[i].litLength);
            nl += s[i].litLength;
            pos += (size_t)s[i].litLength + s[i].matchLength;
        }
    }
    if (open) die("sequences did not end with a block delimiter");
    if (pos != n) die("sequences do not cover the input");
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
    else { fprintf(stderr, "cref: unknown mode %s\n", m); return 2; }
    if (fflush(stdout)) die("write error");
    return rc;
}

/*
 * lz4ref - C reference driver for the lz4 port of fastcompress. Built straight
 * from ref/lz4/lib (lz4 v1.11.1: lz4.c, lz4hc.c, lz4frame.c, xxhash.c), with
 * LZ4F_MULTITHREAD off. Reads stdin, writes stdout. Exit status 0 on success;
 * 2 when a decoder rejects its input (stderr: "ret N" or the LZ4F error name);
 * 1 on a usage or internal error (message on stderr).
 *
 * Arguments are key=value pairs after the mode. Lists are comma separated.
 *
 * fast accel=A [cap=N]
 *   LZ4_compress_fast(src, accel A) into a buffer of capacity N
 *   (default LZ4_compressBound). Output: the block (empty if the result is 0).
 * hc level=L [cap=N] [favor=0|1]
 *   LZ4_compress_HC, or with favor=1 LZ4_compress_HC_extStateHC_fastReset on a
 *   state with LZ4_favorDecompressionSpeed. Output: the block.
 * fastreset accel=A sizes=S1,S2,... [cap=N]
 *   Consecutive pieces of stdin, each compressed with
 *   LZ4_compress_fast_extState_fastReset on one reused state (cap N per block
 *   when given, else the bound). Output: per block, a 4-byte LE length, then it.
 * hcreset level=L sizes=... [cap=N]
 *   The same with LZ4_compress_HC_extStateHC_fastReset.
 * destsize [accel=A] target=T   (fast; accel != 1 uses LZ4_compress_destSize_extState)
 * hcdestsize level=L target=T
 *   Output: 4-byte LE consumed input size, then the block.
 * stream algo=fast|hc level=L sizes=... [layout=prefix|ext|save] [dict=FILE]
 *        [dictmode=load|slow|attach] [cap=bound|tight] [favor=0|1]
 *   LZ4_compress_fast_continue (level = acceleration) or LZ4_compress_HC_continue
 *   over consecutive pieces. layout=prefix: all pieces lie contiguously in one
 *   buffer (after the dictionary when there is one, for dictmode=load|slow);
 *   ext: each piece is copied into its own allocation (extDict mode; the
 *   dictionary stays in its own buffer); save: like ext, and after each block
 *   LZ4_saveDict / LZ4_saveDictHC(64 KB) into a separate buffer (as lz4frame).
 *   dictmode=attach: the dictionary is loaded into a second stream and attached
 *   (LZ4_attach_dictionary / LZ4_attach_HC_dictionary) before the first block.
 *   cap=tight: each block gets capacity size-1 (limitedOutput, may fail: 0).
 *   Output: per block, 4-byte LE length then the block.
 * dec cap=N | decpartial target=T cap=N | decdict dict=FILE cap=N [prefix=1]
 *   LZ4_decompress_safe, _partial, or _usingDict (prefix=1 places the dictionary
 *   right before dst, else in its own buffer). Output: the decoded bytes.
 * frame [level=L] [bsid=4..7] [linked=0|1] [cchk=0|1] [bchk=0|1] [csize=0|1]
 *       [dictid=N] [favor=0|1] [dict=FILE] [cdict=0|1] [autoflush=0|1]
 *       [plan=oneshot|STEP,...]
 *   plan=oneshot (default): LZ4F_compressFrame (or LZ4F_compressFrame_usingCDict
 *   with a dictionary and cdict=1; a dictionary with cdict=0 uses
 *   LZ4F_compressBegin_usingDict + one update + end). Otherwise steps:
 *   wN writes the next N bytes of stdin from a fresh allocation (unstable src),
 *   uN writes N bytes as uncompressed blocks, f flushes, e ends (implied).
 *   Output: the frame.
 * framedec [dict=FILE]
 *   LZ4F_decompress_usingDict over all of stdin (concatenated frames, skippable
 *   frames included). Output: the decoded bytes; error: exit 2, name on stderr.
 * xxh32 [seed=N]
 *   Output: XXH32 of stdin as 8 hex digits.
 * version
 */
#define LZ4_STATIC_LINKING_ONLY
#define LZ4_HC_STATIC_LINKING_ONLY
#define LZ4F_STATIC_LINKING_ONLY
#include "lz4.h"
#include "lz4hc.h"
#include "lz4frame.h"
#include "xxhash.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void die(const char* msg) { fprintf(stderr, "lz4ref: %s\n", msg); exit(1); }

static unsigned char* readAll(FILE* f, size_t* n)
{
    size_t cap = 1 << 16, len = 0;
    unsigned char* b = malloc(cap);
    if (!b) die("oom");
    for (;;) {
        size_t r;
        if (len == cap) { cap *= 2; b = realloc(b, cap); if (!b) die("oom"); }
        r = fread(b + len, 1, cap - len, f);
        if (r == 0) break;
        len += r;
    }
    *n = len;
    return b;
}

static unsigned char* readFile(const char* path, size_t* n)
{
    FILE* f = fopen(path, "rb");
    unsigned char* b;
    if (!f) die("cannot open dictionary");
    b = readAll(f, n);
    fclose(f);
    return b;
}

static int g_argc; static char** g_argv;
static const char* arg(const char* key, const char* def)
{
    size_t kl = strlen(key);
    int i;
    for (i = 2; i < g_argc; i++)
        if (!strncmp(g_argv[i], key, kl) && g_argv[i][kl] == '=') return g_argv[i] + kl + 1;
    return def;
}
static long long argi(const char* key, long long def)
{
    const char* v = arg(key, NULL);
    return v ? strtoll(v, NULL, 0) : def;
}

/* parses a comma separated list of sizes; *count receives the count */
static size_t* sizesArg(size_t* count, size_t total)
{
    const char* v = arg("sizes", NULL);
    size_t* s; size_t n = 1, i = 0, sum = 0;
    const char* p;
    if (!v) { s = malloc(sizeof(*s)); s[0] = total; *count = 1; return s; }
    for (p = v; *p; p++) if (*p == ',') n++;
    s = malloc(n * sizeof(*s));
    p = v;
    while (i < n) { char* e; s[i] = strtoull(p, &e, 0); sum += s[i]; i++; p = (*e == ',') ? e + 1 : e; }
    if (sum != total) die("sizes do not sum to the input length");
    *count = n;
    return s;
}

static void put32(unsigned v) { unsigned char b[4] = { v, v >> 8, v >> 16, v >> 24 }; fwrite(b, 1, 4, stdout); }
static void putBlock(const char* d, int n) { put32((unsigned)n); if (n > 0) fwrite(d, 1, (size_t)n, stdout); }

static int modeFast(const unsigned char* src, size_t n)
{
    int accel = (int)argi("accel", 1);
    int cap = (int)argi("cap", LZ4_compressBound((int)n));
    char* dst = malloc((size_t)cap + 1);
    int r = LZ4_compress_fast((const char*)src, dst, (int)n, cap, accel);
    if (r > 0) fwrite(dst, 1, (size_t)r, stdout);
    return 0;
}

static int modeHC(const unsigned char* src, size_t n)
{
    int level = (int)argi("level", 9);
    int cap = (int)argi("cap", LZ4_compressBound((int)n));
    char* dst = malloc((size_t)cap + 1);
    int r;
    if (argi("favor", 0)) {
        LZ4_streamHC_t* s = LZ4_createStreamHC();
        LZ4_favorDecompressionSpeed(s, 1);
        r = LZ4_compress_HC_extStateHC_fastReset(s, (const char*)src, dst, (int)n, cap, level);
        LZ4_freeStreamHC(s);
    } else {
        r = LZ4_compress_HC((const char*)src, dst, (int)n, cap, level);
    }
    if (r > 0) fwrite(dst, 1, (size_t)r, stdout);
    return 0;
}

static int modeReset(const unsigned char* src, size_t n, int hc)
{
    size_t cnt, i, pos = 0;
    size_t* sizes = sizesArg(&cnt, n);
    LZ4_stream_t* fs = hc ? NULL : LZ4_createStream();
    LZ4_streamHC_t* hs = hc ? LZ4_createStreamHC() : NULL;
    int level = (int)argi(hc ? "level" : "accel", hc ? 9 : 1);
    for (i = 0; i < cnt; i++) {
        int cap = (int)argi("cap", LZ4_compressBound((int)sizes[i]));
        char* dst = malloc((size_t)cap + 1);
        char* in = malloc(sizes[i] + 1);   /* own allocation : no accidental adjacency */
        int r;
        memcpy(in, src + pos, sizes[i]);
        r = hc ? LZ4_compress_HC_extStateHC_fastReset(hs, in, dst, (int)sizes[i], cap, level)
               : LZ4_compress_fast_extState_fastReset(fs, in, dst, (int)sizes[i], cap, level);
        putBlock(dst, r);
        pos += sizes[i];
        free(dst); free(in);
    }
    return 0;
}

static int modeDestSize(const unsigned char* src, size_t n, int hc)
{
    int target = (int)argi("target", 0);
    int consumed = (int)n;
    char* dst = malloc((size_t)target + 1);
    int r;
    if (hc) {
        LZ4_streamHC_t* s = LZ4_createStreamHC();
        r = LZ4_compress_HC_destSize(s, (const char*)src, dst, &consumed, target, (int)argi("level", 9));
        LZ4_freeStreamHC(s);
    } else {
        int accel = (int)argi("accel", 1);
        if (accel == 1) {
            r = LZ4_compress_destSize((const char*)src, dst, &consumed, target);
        } else {
            LZ4_stream_t* s = LZ4_createStream();
            r = LZ4_compress_destSize_extState(s, (const char*)src, dst, &consumed, target, accel);
            LZ4_freeStream(s);
        }
    }
    put32((unsigned)consumed);
    if (r > 0) fwrite(dst, 1, (size_t)r, stdout);
    return 0;
}

static int modeStream(const unsigned char* src, size_t n)
{
    const char* algo = arg("algo", "fast");
    int hc = !strcmp(algo, "hc");
    int level = (int)argi("level", hc ? 9 : 1);
    const char* layout = arg("layout", "prefix");
    const char* dictPath = arg("dict", NULL);
    const char* dictmode = arg("dictmode", "load");
    int tight = !strcmp(arg("cap", "bound"), "tight");
    size_t cnt, i, pos = 0, dictSize = 0;
    size_t* sizes = sizesArg(&cnt, n);
    unsigned char* dict = dictPath ? readFile(dictPath, &dictSize) : NULL;
    LZ4_stream_t* fs = NULL; LZ4_stream_t* fd = NULL;
    LZ4_streamHC_t* hs = NULL; LZ4_streamHC_t* hd = NULL;
    int attach = !strcmp(dictmode, "attach");
    int prefixLayout = !strcmp(layout, "prefix");
    int saveLayout = !strcmp(layout, "save");
    char* big = NULL;   /* prefix layout : dictionary then all pieces */
    const char* dictLoc = (const char*)dict;
    char* saveBuf = malloc(64 * 1024);

    if (prefixLayout) {
        big = malloc(dictSize + n + 1);
        if (dict) memcpy(big, dict, dictSize);
        memcpy(big + dictSize, src, n);
        if (!attach) dictLoc = big;
    }
    if (hc) {
        hs = LZ4_createStreamHC();
        LZ4_setCompressionLevel(hs, level);
        if (argi("favor", 0)) LZ4_favorDecompressionSpeed(hs, 1);
        if (dict) {
            if (attach) {
                hd = LZ4_createStreamHC();
                LZ4_setCompressionLevel(hd, level);
                LZ4_loadDictHC(hd, (const char*)dict, (int)dictSize);
                LZ4_attach_HC_dictionary(hs, hd);
            } else {
                LZ4_loadDictHC(hs, dictLoc, (int)dictSize);
            }
        }
    } else {
        fs = LZ4_createStream();
        if (dict) {
            if (attach) {
                fd = LZ4_createStream();
                LZ4_loadDictSlow(fd, (const char*)dict, (int)dictSize);
                LZ4_resetStream_fast(fs);
                LZ4_attach_dictionary(fs, fd);
            } else if (!strcmp(dictmode, "slow")) {
                LZ4_loadDictSlow(fs, dictLoc, (int)dictSize);
            } else {
                LZ4_loadDict(fs, dictLoc, (int)dictSize);
            }
        }
    }
    for (i = 0; i < cnt; i++) {
        int cap = tight ? (sizes[i] > 1 ? (int)sizes[i] - 1 : 1) : LZ4_compressBound((int)sizes[i]);
        char* dst = malloc((size_t)cap + 1);
        const char* in;
        char* own = NULL;
        int r;
        if (prefixLayout) {
            in = big + dictSize + pos;
        } else {
            own = malloc(sizes[i] + 1);
            memcpy(own, src + pos, sizes[i]);
            in = own;
        }
        r = hc ? LZ4_compress_HC_continue(hs, in, dst, (int)sizes[i], cap)
               : LZ4_compress_fast_continue(fs, in, dst, (int)sizes[i], cap, level);
        putBlock(dst, r);
        if (saveLayout) {
            if (hc) LZ4_saveDictHC(hs, saveBuf, 64 * 1024);
            else LZ4_saveDict(fs, saveBuf, 64 * 1024);
        }
        /* ext layout : the previous piece must stay readable (it is the history) */
        pos += sizes[i];
        free(dst);
        (void)own;
    }
    return 0;
}

static int decResult(int r, const char* dst)
{
    if (r < 0) { fprintf(stderr, "ret %d\n", r); return 2; }
    fwrite(dst, 1, (size_t)r, stdout);
    return 0;
}

static int modeDec(const unsigned char* src, size_t n, int kind)
{
    int cap = (int)argi("cap", 0);
    char* dst;
    if (kind == 0) {
        dst = malloc((size_t)cap + 1);
        return decResult(LZ4_decompress_safe((const char*)src, dst, (int)n, cap), dst);
    }
    if (kind == 1) {
        int target = (int)argi("target", 0);
        dst = malloc((size_t)cap + 1);
        return decResult(LZ4_decompress_safe_partial((const char*)src, dst, (int)n, target, cap), dst);
    }
    {   size_t dictSize = 0;
        const char* dp = arg("dict", NULL);
        unsigned char* dict = dp ? readFile(dp, &dictSize) : NULL;
        if (argi("prefix", 0)) {
            char* buf = malloc(dictSize + (size_t)cap + 1);
            if (dictSize) memcpy(buf, dict, dictSize);
            dst = buf + dictSize;
            return decResult(LZ4_decompress_safe_usingDict((const char*)src, dst, (int)n, cap, buf, (int)dictSize), dst);
        }
        dst = malloc((size_t)cap + 1);
        return decResult(LZ4_decompress_safe_usingDict((const char*)src, dst, (int)n, cap, (const char*)dict, (int)dictSize), dst);
    }
}

static void prefsFromArgs(LZ4F_preferences_t* p)
{
    memset(p, 0, sizeof(*p));
    p->compressionLevel = (int)argi("level", 0);
    p->frameInfo.blockSizeID = (LZ4F_blockSizeID_t)argi("bsid", 0);
    p->frameInfo.blockMode = argi("linked", 0) ? LZ4F_blockLinked : LZ4F_blockIndependent;
    p->frameInfo.contentChecksumFlag = argi("cchk", 0) ? LZ4F_contentChecksumEnabled : LZ4F_noContentChecksum;
    p->frameInfo.blockChecksumFlag = argi("bchk", 0) ? LZ4F_blockChecksumEnabled : LZ4F_noBlockChecksum;
    p->frameInfo.dictID = (unsigned)argi("dictid", 0);
    p->favorDecSpeed = (unsigned)argi("favor", 0);
    p->autoFlush = (unsigned)argi("autoflush", 0);
}

#define CHECKF(r) do { if (LZ4F_isError(r)) { fprintf(stderr, "lz4ref: %s\n", LZ4F_getErrorName(r)); exit(1); } } while (0)

static int modeFrame(const unsigned char* src, size_t n)
{
    LZ4F_preferences_t prefs;
    const char* plan = arg("plan", "oneshot");
    const char* dictPath = arg("dict", NULL);
    size_t dictSize = 0;
    unsigned char* dict = dictPath ? readFile(dictPath, &dictSize) : NULL;
    int useCDict = (int)argi("cdict", 1);
    LZ4F_CDict* cdict = (dict && useCDict) ? LZ4F_createCDict(dict, dictSize) : NULL;
    LZ4F_cctx* cctx;
    size_t cap, pos = 0, done = 0;
    unsigned char* dst;
    prefsFromArgs(&prefs);
    if (argi("csize", 0)) prefs.frameInfo.contentSize = n;
    CHECKF(LZ4F_createCompressionContext(&cctx, LZ4F_VERSION));
    cap = LZ4F_compressFrameBound(n, &prefs) + LZ4F_compressBound(n, &prefs) + 4096;
    dst = malloc(cap);
    if (!strcmp(plan, "oneshot") && (!dict || useCDict)) {
        size_t r = dict ? LZ4F_compressFrame_usingCDict(cctx, dst, cap, src, n, cdict, &prefs)
                        : LZ4F_compressFrame(dst, cap, src, n, &prefs);
        CHECKF(r);
        fwrite(dst, 1, r, stdout);
        return 0;
    }
    {   size_t r = cdict ? LZ4F_compressBegin_usingCDict(cctx, dst, cap, cdict, &prefs)
                 : dict ? LZ4F_compressBegin_usingDict(cctx, dst, cap, dict, dictSize, &prefs)
                 : LZ4F_compressBegin(cctx, dst, cap, &prefs);
        CHECKF(r); pos += r;
    }
    if (!strcmp(plan, "oneshot")) {
        LZ4F_compressOptions_t o; memset(&o, 0, sizeof(o));
        {   size_t r = LZ4F_compressUpdate(cctx, dst + pos, cap - pos, src, n, &o); CHECKF(r); pos += r; }
    } else {
        const char* p = plan;
        while (*p) {
            char c = *p++;
            if (c == 'w' || c == 'u') {
                char* e;
                size_t sz = strtoull(p, &e, 10);
                unsigned char* own = malloc(sz + 1);
                LZ4F_compressOptions_t o; memset(&o, 0, sizeof(o));
                p = e;
                if (done + sz > n) die("plan exceeds input");
                memcpy(own, src + done, sz);
                o.uncompressed = (c == 'u');
                {   size_t r = LZ4F_compressUpdate(cctx, dst + pos, cap - pos, own, sz, &o); CHECKF(r); pos += r; }
                free(own);   /* unstable source : the buffer is gone after the call */
                done += sz;
            } else if (c == 'f') {
                size_t r = LZ4F_flush(cctx, dst + pos, cap - pos, NULL); CHECKF(r); pos += r;
            } else if (c == 'e') {
                break;
            }
            if (*p == ',') p++;
        }
        if (done != n) die("plan does not cover the input");
    }
    {   size_t r = LZ4F_compressEnd(cctx, dst + pos, cap - pos, NULL); CHECKF(r); pos += r; }
    fwrite(dst, 1, pos, stdout);
    return 0;
}

static int modeFrameDec(const unsigned char* src, size_t n)
{
    LZ4F_dctx* d;
    size_t dictSize = 0, sp = 0;
    const char* dp = arg("dict", NULL);
    unsigned char* dict = dp ? readFile(dp, &dictSize) : NULL;
    size_t outCap = 1 << 20;
    unsigned char* out = malloc(outCap);
    size_t ret = n ? 1 : 0;   /* no input : no frame */
    CHECKF(LZ4F_createDecompressionContext(&d, LZ4F_VERSION));
    while (sp < n) {
        size_t dsz = outCap, ssz = n - sp;
        ret = LZ4F_decompress_usingDict(d, out, &dsz, src + sp, &ssz, dict, dictSize, NULL);
        if (LZ4F_isError(ret)) { fflush(stdout); fprintf(stderr, "%s\n", LZ4F_getErrorName(ret)); return 2; }
        fwrite(out, 1, dsz, stdout);
        sp += ssz;
        if (ssz == 0 && dsz == 0) break;
    }
    /* drain output still buffered within the context */
    for (;;) {
        size_t dsz = outCap, ssz = 0;
        if (ret == 0) break;
        ret = LZ4F_decompress_usingDict(d, out, &dsz, NULL, &ssz, dict, dictSize, NULL);
        if (LZ4F_isError(ret)) { fflush(stdout); fprintf(stderr, "%s\n", LZ4F_getErrorName(ret)); return 2; }
        if (dsz == 0) break;
        fwrite(out, 1, dsz, stdout);
    }
    if (ret != 0) { fflush(stdout); fprintf(stderr, "incomplete\n"); return 2; }
    return 0;
}

int main(int argc, char** argv)
{
    size_t n;
    unsigned char* src;
    const char* mode;
    g_argc = argc; g_argv = argv;
    if (argc < 2) die("usage: lz4ref MODE [key=value...]");
    mode = argv[1];
    if (!strcmp(mode, "version")) { printf("%s\n", LZ4_versionString()); return 0; }
    src = readAll(stdin, &n);
    if (!strcmp(mode, "xxh32")) { printf("%08x\n", XXH32(src, n, (unsigned)argi("seed", 0))); return 0; }
    if (!strcmp(mode, "fast")) return modeFast(src, n);
    if (!strcmp(mode, "hc")) return modeHC(src, n);
    if (!strcmp(mode, "fastreset")) return modeReset(src, n, 0);
    if (!strcmp(mode, "hcreset")) return modeReset(src, n, 1);
    if (!strcmp(mode, "destsize")) return modeDestSize(src, n, 0);
    if (!strcmp(mode, "hcdestsize")) return modeDestSize(src, n, 1);
    if (!strcmp(mode, "stream")) return modeStream(src, n);
    if (!strcmp(mode, "dec")) return modeDec(src, n, 0);
    if (!strcmp(mode, "decpartial")) return modeDec(src, n, 1);
    if (!strcmp(mode, "decdict")) return modeDec(src, n, 2);
    if (!strcmp(mode, "frame")) return modeFrame(src, n);
    if (!strcmp(mode, "framedec")) return modeFrameDec(src, n);
    die("unknown mode");
    return 1;
}

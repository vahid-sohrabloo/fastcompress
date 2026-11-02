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
 *   -dict FILE      dictionary file (ZSTD_dct_auto unless -raw-dict 1), loaded
 *                   once into the context with ZSTD_CCtx_loadDictionary_advanced
 *                   (ZSTD_dlm_byCopy): C digests it into a local CDict from the
 *                   context's parameters at the first frame (ZSTD_initLocalDict)
 *                   and attaches or copies that CDict for every frame, as
 *                   fastzstd's WithEncoderDict / WithEncoderDictRaw. A raw
 *                   dictionary has ID 0 (C cannot write another ID).
 *   -raw-dict 0|1   1: load the dictionary as ZSTD_dct_rawContent
 *   -lit-mode M     ZSTD_c_literalCompressionMode: 0 auto (default),
 *                   1 ZSTD_ps_enable, 2 ZSTD_ps_disable
 *   -cparams W,C,H,S,M,T,STRAT
 *                   ZSTD_c_windowLog, chainLog, hashLog, searchLog, minMatch,
 *                   targetLength, strategy, set after the level (0 = the
 *                   level's value)
 *   -row R          ZSTD_c_useRowMatchFinder: 0 auto (default), 1 enable,
 *                   2 disable
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
 * compress-seq [common opts] -sizes N1,N2,... [-levels L1,L2,...]
 *              [-checksums C1,C2,...] [-window-logs W1,W2,...] [-salt-out FILE]
 *   Compresses consecutive pieces of stdin (sizes N1, N2, ..., which must sum
 *   to the input length; 0 is an empty piece) one after another through ONE
 *   ZSTD_CCtx, each with ZSTD_compress2 and a ZSTD_compressBound(Ni) output
 *   buffer. The common opts configure the context once; before step i, the
 *   i-th entry of each given list is applied with ZSTD_CCtx_setParameter
 *   (ZSTD_c_compressionLevel, ZSTD_c_checksumFlag, ZSTD_c_windowLog; a window
 *   log of 0 restores the level default). A list shorter than the number of
 *   pieces leaves later steps unchanged. This reproduces a reused context:
 *   e.g. the row-based match finder (greedy..lazy2 with windowLog > 14)
 *   advances its hash salt on every context reset; compress-seq reproduces
 *   that state, and the output equals a fresh context's (verified: the salt
 *   only relabels rows and tags, so it cannot change the frames).
 *   Output: for each frame, its length as a 4-byte little-endian integer,
 *   then the frame.
 *   -salt-out FILE: after each step, append a text line to FILE with the
 *   row match finder's state of the context's match state:
 *   "<hashSalt as 16 hex digits> <hashSaltEntropy decimal>\n".
 *
 * stream [common opts, -pledged default 0] -chunks N1,N2,... -flush-after i,j,...
 *   Feeds the input in chunks of the given sizes (must sum to the input length;
 *   a 0 size means an empty ZSTD_e_continue call) with ZSTD_e_continue; after
 *   chunk index k (0-based) listed in -flush-after, loops ZSTD_e_flush (empty
 *   input) until it returns 0; finally loops ZSTD_e_end until 0.
 *   -pledged 1 calls ZSTD_CCtx_setPledgedSrcSize(total input length) first.
 *   -pledged-size N calls ZSTD_CCtx_setPledgedSrcSize(N) first instead (N may
 *   differ from the input length: C then fails with srcSize_wrong, cref exits
 *   1 and prints the error name on stderr).
 *   Output: the zstd frame (raw bytes).
 *
 * stream-pipe [common opts] -chunk-size N [-flush-every K] [-pledged-size N]
 *   Like stream, but never holds the input or the output in memory: it reads
 *   stdin N bytes at a time (each read is one ZSTD_e_continue chunk; the last
 *   one may be shorter; an empty input feeds no chunk), loops ZSTD_e_flush
 *   after every K-th chunk (K >= 1; absent: never), then loops ZSTD_e_end,
 *   writing the output to stdout after every ZSTD_compressStream2 call. The
 *   content size is unknown unless -pledged-size is given. For inputs of any
 *   length (e.g. the multi-GB overflow test) and chunk lists too long for
 *   argv (1-byte chunks).
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
 * decompress [-dict FILE] [-raw-dict 0|1] [-max-window-log W] [-ignore-checksum 0|1]
 *   One ZSTD_DCtx: ZSTD_DCtx_setParameter(ZSTD_d_windowLogMax, W) when W is
 *   given, ZSTD_d_forceIgnoreChecksum when -ignore-checksum 1; a dictionary is
 *   a ZSTD_createDDict_advanced (ZSTD_dlm_byCopy, ZSTD_dct_auto or
 *   ZSTD_dct_rawContent) attached with ZSTD_DCtx_refDDict. Then one
 *   ZSTD_decompressDCtx over all of stdin (every frame, skippable frames
 *   skipped) into a buffer of ZSTD_decompressBound(stdin) bytes; when the bound
 *   is an error, the buffer starts at 1 MiB and doubles (up to 1 GiB) while the
 *   call fails with dstSize_tooSmall.
 *   Output: the decompressed bytes. On error nothing is written to stdout;
 *   stderr gets "ERR <ZSTD_getErrorCode> <ZSTD_getErrorName>\n", exit 1.
 *
 * dstream [decompress opts] (-chunks N1,N2,... | -chunk-size N) -out-size M
 *   The same DCtx setup, then ZSTD_decompressStream with the input fed in the
 *   given chunks (-chunks must sum to the input length; a 0 is one call with
 *   empty input; -chunk-size N cuts the input into N-byte chunks, the last one
 *   shorter) and an output buffer of exactly M bytes. Each chunk is offered
 *   until it is consumed and the output buffer comes back not full. Stdout
 *   gets the output of every call as it is produced, including the bytes
 *   before an error. On error: the ERR line of decompress, exit 1. When the
 *   input ends inside a frame (the last call that consumed input or produced
 *   output returned > 0) stderr gets "INCOMPLETE <that value>\n", exit 1.
 *   ZSTD_decompressStream not moving input or output while input is left
 *   is a cref failure ("cref: dstream: ... no progress", exit 1), never a spin.
 *   NOTE: C applies ZSTD_d_windowLogMax only here, never in decompress.
 *   NOTE (decompress vs dstream on short input):
 *     empty input:  both succeed with empty output (no frames);
 *     a truncated frame, down to 1 byte: decompress fails with
 *     srcSize_wrong ("ERR 72 Src size is incorrect"; a cut in or right before the
 *     4-byte checksum gives checksum_wrong, 22); dstream writes what it
 *     decoded and reports INCOMPLETE (C keeps asking for input).
 *
 * fheader              Frame header queries on stdin, as key=value lines:
 *   ZSTD_getFrameHeader_advanced(ZSTD_f_zstd1): "err=<code>" and
 *   "errName=<name>" on error, else "needed=<rc>" (0: header complete) and,
 *   when rc == 0, frameContentSize windowSize blockSizeMax frameType
 *   headerSize dictID checksumFlag (decimal; frameContentSize UNKNOWN is
 *   18446744073709551615). Then "compressedSize=" ZSTD_findFrameCompressedSize
 *   ("err:<code>" on error) and "decompressBound=" ZSTD_decompressBound (raw
 *   value; ZSTD_CONTENTSIZE_ERROR is 18446744073709551614). For a complete
 *   zstd (not skippable) header, the first block header is decoded with
 *   ZSTD_getcBlockSize: "blockType=" (0 raw, 1 rle, 2 compressed, 3 reserved),
 *   "blockSize=" (return value, "err:<code>" on error), "blockOrigSize=" and
 *   "lastBlock=".
 *
 * skippable -variant V ZSTD_writeSkippableFrame(stdin, magicVariant V).
 *   Output: the skippable frame.
 *
 * finalize-dict -level L -dict-id N -content FILE -sizes N1,N2,...
 *   ZDICT_finalizeDictionary with the content of FILE, the samples
 *   concatenated on stdin (sizes N1.. must sum to the input length), params
 *   { L, notificationLevel 0, N } and a capacity of content size + 64 KiB (so
 *   the content is never truncated). Output: the dictionary. On error: the
 *   ERR line of decompress, exit 1.
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
#include "zdict.h"

extern void (*ZSTD_crefHook)(const SeqStore_t*, size_t, int); /* seqdump.patch */

static void die(const char* msg) { fprintf(stderr, "cref: %s\n", msg); exit(1); }
static void dieZ(const char* what, size_t rc) {
    fprintf(stderr, "cref: %s: %s\n", what, ZSTD_getErrorName(rc)); exit(1);
}
/* dieErr is the decoder modes' error report: "ERR <code> <name>". */
static void dieErr(size_t rc) {
    fflush(stdout);
    fprintf(stderr, "ERR %d %s\n", (int)ZSTD_getErrorCode(rc), ZSTD_getErrorName(rc)); exit(1);
}

typedef struct {
    int level, checksum, pledged, windowLog, rawDict, tablelog, flags, litMode, row;
    const char* cparams;
    const char* dictFile;
    unsigned long long seed;
    const char* chunks;
    const char* flushAfter;
    const char* frameOut;
    const char *sizes, *levels, *checksums, *windowLogs, *saltOut;
    long long pledgedSize; /* -1: not given */
    size_t chunkSize, flushEvery;
    int maxWindowLog, ignoreChecksum, variant;
    size_t outSize;
    unsigned dictID;
    const char* contentFile;
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
    o->level = 3; o->pledged = defaultPledged; o->pledgedSize = -1;
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
        else if (!strcmp(a, "-cparams"))    o->cparams = v;
        else if (!strcmp(a, "-row"))        o->row = atoi(v);
        else if (!strcmp(a, "-tablelog"))   o->tablelog = atoi(v);
        else if (!strcmp(a, "-flags"))      o->flags = atoi(v);
        else if (!strcmp(a, "-seed"))       o->seed = strtoull(v, NULL, 0);
        else if (!strcmp(a, "-chunks"))     o->chunks = v;
        else if (!strcmp(a, "-flush-after")) o->flushAfter = v;
        else if (!strcmp(a, "-frame-out"))  o->frameOut = v;
        else if (!strcmp(a, "-sizes"))      o->sizes = v;
        else if (!strcmp(a, "-levels"))     o->levels = v;
        else if (!strcmp(a, "-checksums"))  o->checksums = v;
        else if (!strcmp(a, "-window-logs")) o->windowLogs = v;
        else if (!strcmp(a, "-salt-out"))   o->saltOut = v;
        else if (!strcmp(a, "-pledged-size")) o->pledgedSize = strtoll(v, NULL, 10);
        else if (!strcmp(a, "-chunk-size")) o->chunkSize = (size_t)strtoull(v, NULL, 10);
        else if (!strcmp(a, "-flush-every")) o->flushEvery = (size_t)strtoull(v, NULL, 10);
        else if (!strcmp(a, "-max-window-log")) o->maxWindowLog = atoi(v);
        else if (!strcmp(a, "-ignore-checksum")) o->ignoreChecksum = atoi(v);
        else if (!strcmp(a, "-out-size"))   o->outSize = (size_t)strtoull(v, NULL, 10);
        else if (!strcmp(a, "-variant"))    o->variant = atoi(v);
        else if (!strcmp(a, "-dict-id"))    o->dictID = (unsigned)strtoul(v, NULL, 10);
        else if (!strcmp(a, "-content"))    o->contentFile = v;
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
    if (o->cparams) {
        static const ZSTD_cParameter ps[7] = { ZSTD_c_windowLog, ZSTD_c_chainLog,
            ZSTD_c_hashLog, ZSTD_c_searchLog, ZSTD_c_minMatch, ZSTD_c_targetLength,
            ZSTD_c_strategy };
        int v[7];
        if (sscanf(o->cparams, "%d,%d,%d,%d,%d,%d,%d", &v[0], &v[1], &v[2], &v[3],
                   &v[4], &v[5], &v[6]) != 7) die("-cparams needs 7 values");
        for (int i = 0; i < 7; i++) {
            r = ZSTD_CCtx_setParameter(c, ps[i], v[i]);
            if (ZSTD_isError(r)) dieZ("set cparams", r);
        }
    }
    if (o->row) {
        r = ZSTD_CCtx_setParameter(c, ZSTD_c_useRowMatchFinder, o->row);
        if (ZSTD_isError(r)) dieZ("set useRowMatchFinder", r);
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
    if (o->pledged || o->pledgedSize >= 0) {
        size_t r = ZSTD_CCtx_setPledgedSrcSize(c, o->pledgedSize >= 0 ? (unsigned long long)o->pledgedSize : n);
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

/* feedOut is feed() writing each call's output straight to stdout. */
static void feedOut(ZSTD_CCtx* c, ZSTD_EndDirective op, const void* in, size_t n) {
    static unsigned char* buf;
    const size_t bsz = ZSTD_CStreamOutSize();
    ZSTD_inBuffer ib = { in, n, 0 };
    if (!buf && !(buf = malloc(bsz))) die("oom");
    for (;;) {
        ZSTD_outBuffer ob = { buf, bsz, 0 };
        size_t r = ZSTD_compressStream2(c, &ob, &ib, op);
        if (ZSTD_isError(r)) dieZ("ZSTD_compressStream2", r);
        writeAll(buf, ob.pos);
        if (op == ZSTD_e_continue) { if (ib.pos == ib.size) break; }
        else if (r == 0) break;
    }
}

static int cmdStreamPipe(const Opts* o) {
    if (!o->chunkSize) die("stream-pipe needs -chunk-size");
    ZSTD_CCtx* c = newCCtx(o);
    if (o->pledgedSize >= 0) {
        size_t r = ZSTD_CCtx_setPledgedSrcSize(c, (unsigned long long)o->pledgedSize);
        if (ZSTD_isError(r)) dieZ("setPledgedSrcSize", r);
    }
    unsigned char* chunk = malloc(o->chunkSize);
    if (!chunk) die("oom");
    for (size_t idx = 1;; idx++) {
        size_t r = fread(chunk, 1, o->chunkSize, stdin);
        if (r == 0) break;
        feedOut(c, ZSTD_e_continue, chunk, r);
        if (o->flushEvery && idx % o->flushEvery == 0) feedOut(c, ZSTD_e_flush, chunk, 0);
        if (r < o->chunkSize) break;
    }
    if (ferror(stdin)) die("read error");
    feedOut(c, ZSTD_e_end, chunk, 0);
    return 0;
}

/* listAt returns the idx-th (0-based) comma-separated entry of list as an
 * int in *v, or 0 if list is NULL or shorter. */
static int listAt(const char* list, size_t idx, int* v) {
    char* s = list ? strdup(list) : NULL;
    char* save = NULL;
    size_t i = 0;
    if (!s) return 0;
    for (char* t = strtok_r(s, ",", &save); t; t = strtok_r(NULL, ",", &save), i++)
        if (i == idx) { *v = atoi(t); free(s); return 1; }
    free(s);
    return 0;
}

static void writeLE32(size_t n) {
    unsigned char b[4] = { (unsigned char)n, (unsigned char)(n >> 8), (unsigned char)(n >> 16), (unsigned char)(n >> 24) };
    writeAll(b, 4);
}

static int cmdCompressSeq(const Opts* o, const unsigned char* src, size_t n) {
    if (!o->sizes) die("compress-seq needs -sizes");
    ZSTD_CCtx* c = newCCtx(o);
    FILE* salts = o->saltOut ? fopen(o->saltOut, "w") : NULL;
    char* ss = strdup(o->sizes);
    char* save = NULL;
    size_t pos = 0, idx = 0;
    for (char* t = strtok_r(ss, ",", &save); t; t = strtok_r(NULL, ",", &save), idx++) {
        size_t sz = (size_t)strtoull(t, NULL, 10);
        int v;
        size_t r;
        if (sz > n - pos) die("sizes exceed input length");
        if (listAt(o->levels, idx, &v)) {
            r = ZSTD_CCtx_setParameter(c, ZSTD_c_compressionLevel, v);
            if (ZSTD_isError(r)) dieZ("set level", r);
        }
        if (listAt(o->checksums, idx, &v)) {
            r = ZSTD_CCtx_setParameter(c, ZSTD_c_checksumFlag, v);
            if (ZSTD_isError(r)) dieZ("set checksum", r);
        }
        if (listAt(o->windowLogs, idx, &v)) {
            r = ZSTD_CCtx_setParameter(c, ZSTD_c_windowLog, v);
            if (ZSTD_isError(r)) dieZ("set windowLog", r);
        }
        {   size_t cap = ZSTD_compressBound(sz);
            unsigned char* out = malloc(cap ? cap : 1);
            if (!out) die("oom");
            r = ZSTD_compress2(c, out, cap, src + pos, sz);
            if (ZSTD_isError(r)) dieZ("ZSTD_compress2", r);
            writeLE32(r);
            writeAll(out, r);
            free(out);
        }
        if (salts) fprintf(salts, "%016llx %u\n", (unsigned long long)c->blockState.matchState.hashSalt,
                           (unsigned)c->blockState.matchState.hashSaltEntropy);
        pos += sz;
    }
    free(ss);
    if (pos != n) die("sizes do not cover input length");
    if (o->saltOut && (!salts || fclose(salts))) die("cannot write -salt-out");
    ZSTD_freeCCtx(c);
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

static void* readFile(const char* path, size_t* n) {
    FILE* f = fopen(path, "rb");
    if (!f) die("cannot open file");
    void* b = readAll(f, n);
    fclose(f);
    return b;
}

static ZSTD_DCtx* newDCtx(const Opts* o) {
    ZSTD_DCtx* d = ZSTD_createDCtx();
    if (!d) die("ZSTD_createDCtx");
    size_t r;
    if (o->maxWindowLog) {
        r = ZSTD_DCtx_setParameter(d, ZSTD_d_windowLogMax, o->maxWindowLog);
        if (ZSTD_isError(r)) dieErr(r);
    }
    if (o->ignoreChecksum) {
        r = ZSTD_DCtx_setParameter(d, ZSTD_d_forceIgnoreChecksum, ZSTD_d_ignoreChecksum);
        if (ZSTD_isError(r)) dieErr(r);
    }
    if (o->dictFile) {
        size_t dn; void* db = readFile(o->dictFile, &dn);
        ZSTD_DDict* dd = ZSTD_createDDict_advanced(db, dn, ZSTD_dlm_byCopy,
                o->rawDict ? ZSTD_dct_rawContent : ZSTD_dct_auto, ZSTD_defaultCMem);
        if (!dd) die("ZSTD_createDDict_advanced");
        free(db); /* dd is never freed: it must outlive the DCtx, and cref exits */
        r = ZSTD_DCtx_refDDict(d, dd);
        if (ZSTD_isError(r)) dieErr(r);
    }
    return d;
}

static int cmdDecompress(const Opts* o, const unsigned char* src, size_t n) {
    ZSTD_DCtx* d = newDCtx(o);
    unsigned long long bound = ZSTD_decompressBound(src, n);
    int known = bound != ZSTD_CONTENTSIZE_ERROR;
    size_t cap = known ? (size_t)bound : (size_t)1 << 20;
    for (;;) {
        unsigned char* dst = malloc(cap ? cap : 1);
        if (!dst) die("oom");
        size_t r = ZSTD_decompressDCtx(d, dst, cap, src, n);
        if (!ZSTD_isError(r)) { writeAll(dst, r); return 0; }
        free(dst);
        if (known || ZSTD_getErrorCode(r) != ZSTD_error_dstSize_tooSmall || cap >= (size_t)1 << 30)
            dieErr(r);
        cap *= 2;
    }
}

static int cmdDStream(const Opts* o, const unsigned char* src, size_t n) {
    if (!o->outSize) die("dstream needs -out-size");
    if (!o->chunks == !o->chunkSize) die("dstream needs one of -chunks and -chunk-size");
    ZSTD_DCtx* d = newDCtx(o);
    unsigned char* buf = malloc(o->outSize);
    if (!buf) die("oom");
    char* cs = o->chunks ? strdup(o->chunks) : NULL;
    char* save = NULL;
    char* t = cs ? strtok_r(cs, ",", &save) : NULL;
    size_t pos = 0, last = 0;
    for (;;) {
        size_t sz;
        if (cs) {
            if (!t) break;
            sz = (size_t)strtoull(t, NULL, 10);
            if (sz > n - pos) die("chunks exceed input length");
            t = strtok_r(NULL, ",", &save);
        } else {
            if (pos == n) break;
            sz = n - pos < o->chunkSize ? n - pos : o->chunkSize;
        }
        ZSTD_inBuffer ib = { src + pos, sz, 0 };
        ZSTD_outBuffer ob;
        do {
            ob.dst = buf; ob.size = o->outSize; ob.pos = 0;
            size_t in0 = ib.pos;
            size_t r = ZSTD_decompressStream(d, &ob, &ib);
            writeAll(buf, ob.pos);
            if (ZSTD_isError(r)) dieErr(r);
            if (ib.pos != in0 || ob.pos) last = r; /* a call without progress (e.g. after a frame end) says nothing */
            else if (ib.pos < ib.size) die("dstream: ZSTD_decompressStream made no progress"); /* never spin */
        } while (ib.pos < ib.size || ob.pos == ob.size);
        pos += sz;
    }
    if (pos != n) die("chunks do not cover input length");
    if (last) {
        fflush(stdout);
        fprintf(stderr, "INCOMPLETE %zu\n", last);
        exit(1);
    }
    return 0;
}

static int cmdFHeader(const unsigned char* src, size_t n) {
    ZSTD_FrameHeader h;
    size_t rc = ZSTD_getFrameHeader_advanced(&h, src, n, ZSTD_f_zstd1);
    if (ZSTD_isError(rc)) {
        printf("err=%d\nerrName=%s\n", (int)ZSTD_getErrorCode(rc), ZSTD_getErrorName(rc));
    } else {
        printf("needed=%zu\n", rc);
        if (rc == 0)
            printf("frameContentSize=%llu\nwindowSize=%llu\nblockSizeMax=%u\nframeType=%d\n"
                   "headerSize=%u\ndictID=%u\nchecksumFlag=%u\n",
                   h.frameContentSize, h.windowSize, h.blockSizeMax, (int)h.frameType,
                   h.headerSize, h.dictID, h.checksumFlag);
    }
    size_t cs = ZSTD_findFrameCompressedSize(src, n);
    if (ZSTD_isError(cs)) printf("compressedSize=err:%d\n", (int)ZSTD_getErrorCode(cs));
    else printf("compressedSize=%zu\n", cs);
    printf("decompressBound=%llu\n", ZSTD_decompressBound(src, n));
    if (!ZSTD_isError(rc) && rc == 0 && h.frameType == ZSTD_frame) {
        blockProperties_t bp;
        size_t bs = ZSTD_getcBlockSize(src + h.headerSize, n - h.headerSize, &bp);
        if (ZSTD_isError(bs)) printf("blockSize=err:%d\n", (int)ZSTD_getErrorCode(bs));
        else printf("blockType=%d\nblockSize=%zu\nblockOrigSize=%u\nlastBlock=%u\n",
                    (int)bp.blockType, bs, bp.origSize, bp.lastBlock);
    }
    return 0;
}

static int cmdSkippable(const Opts* o, const unsigned char* src, size_t n) {
    size_t cap = n + ZSTD_SKIPPABLEHEADERSIZE;
    unsigned char* dst = malloc(cap);
    if (!dst) die("oom");
    size_t r = ZSTD_writeSkippableFrame(dst, cap, src, n, (unsigned)o->variant);
    if (ZSTD_isError(r)) dieErr(r);
    writeAll(dst, r);
    return 0;
}

static int cmdFinalizeDict(const Opts* o, const unsigned char* src, size_t n) {
    if (!o->contentFile || !o->sizes) die("finalize-dict needs -content and -sizes");
    size_t cn; void* content = readFile(o->contentFile, &cn);
    size_t nb = 1;
    for (const char* p = o->sizes; *p; p++) nb += *p == ',';
    size_t* sizes = malloc(nb * sizeof(size_t));
    char* ss = strdup(o->sizes);
    if (!sizes || !ss) die("oom");
    char* save = NULL;
    size_t i = 0, tot = 0;
    for (char* t = strtok_r(ss, ",", &save); t; t = strtok_r(NULL, ",", &save))
        tot += sizes[i++] = (size_t)strtoull(t, NULL, 10);
    if (tot != n) die("sizes do not cover input length");
    size_t cap = cn + (64 << 10);
    unsigned char* dst = malloc(cap);
    if (!dst) die("oom");
    ZDICT_params_t p = { o->level, 0, o->dictID };
    size_t r = ZDICT_finalizeDictionary(dst, cap, content, cn, src, sizes, (unsigned)i, p);
    if (ZDICT_isError(r)) dieErr(r);
    writeAll(dst, r);
    return 0;
}

int main(int argc, char** argv) {
    if (argc < 2) { fprintf(stderr, "usage: cref <mode> [options] < in > out\n"); return 2; }
    const char* m = argv[1];
    Opts o;
    size_t n;
    int rc;
    parseOpts(argc, argv, &o, !strcmp(m, "stream") ? 0 : 1);
    if (!strcmp(m, "stream-pipe")) {
        rc = cmdStreamPipe(&o);
        if (fflush(stdout)) die("write error");
        return rc;
    }
    unsigned char* src = readAll(stdin, &n);
    if (!strcmp(m, "compress"))      rc = cmdCompress(&o, src, n);
    else if (!strcmp(m, "stream"))   rc = cmdStream(&o, src, n);
    else if (!strcmp(m, "compress-seq")) rc = cmdCompressSeq(&o, src, n);
    else if (!strcmp(m, "xxh64"))    rc = cmdXxh64(&o, src, n);
    else if (!strcmp(m, "hist"))     rc = cmdHist(src, n);
    else if (!strcmp(m, "fse"))      rc = cmdFse(&o, src, n);
    else if (!strcmp(m, "huf1x"))    rc = cmdHuf(&o, src, n, 1);
    else if (!strcmp(m, "huf4x"))    rc = cmdHuf(&o, src, n, 4);
    else if (!strcmp(m, "seqs"))     rc = cmdSeqs(&o, src, n);
    else if (!strcmp(m, "presplit")) rc = cmdPresplit(&o, src, n);
    else if (!strcmp(m, "decompress")) rc = cmdDecompress(&o, src, n);
    else if (!strcmp(m, "dstream"))  rc = cmdDStream(&o, src, n);
    else if (!strcmp(m, "fheader"))  rc = cmdFHeader(src, n);
    else if (!strcmp(m, "skippable")) rc = cmdSkippable(&o, src, n);
    else if (!strcmp(m, "finalize-dict")) rc = cmdFinalizeDict(&o, src, n);
    else { fprintf(stderr, "cref: unknown mode %s\n", m); return 2; }
    if (fflush(stdout)) die("write error");
    return rc;
}

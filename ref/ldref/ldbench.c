// ldbench: single-thread libdeflate compress/decompress throughput.
// usage: ldbench -mode c|d -level N -secs S [-format raw|gzip] [-block B] [-dumpz PATH] FILE   (-dumpz excludes -mode)
// -dumpz PATH: compress (and verify the round trip), write the per-block streams
// to PATH as records "u64 LE length, bytes" in block order, and exit without
// timing. gzbench -zin reads this so a decode-only library decodes C's streams.
// prints "MB/s ratio bytes" (1e6 bytes/s of uncompressed data; ratio = in/out;
// bytes = exact total compressed size, used for the size-identity check).
// Timing is the same as gzbench.c (fastzstd-gzbench): windows of passes sized
// to ~1s, window time is the mean per pass, windows < 0.5s are discarded, best
// window reported. -block B splits the input into independent B-byte blocks,
// each (de)compressed with one reused compressor/decompressor.
#include <errno.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <libdeflate.h>

static double now(void) {
	struct timespec t; clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec + t.tv_nsec * 1e-9;
}
static void die(const char *m) { fprintf(stderr, "ldbench: %s\n", m); exit(1); }
static void usage_die(const char *what, const char *arg) {
	fprintf(stderr, "ldbench: invalid value for %s: %s\n", what, arg); exit(2);
}
static long parse_num(const char *what, const char *s, long lo, long hi) {
	char *end; errno = 0; long v = strtol(s, &end, 10);
	if (end == s || *end || errno == ERANGE || v < lo || v > hi) usage_die(what, s);
	return v;
}
static double parse_secs(const char *s) {
	char *end; errno = 0; double v = strtod(s, &end);
	if (end == s || *end || errno == ERANGE || !(v > 0 && v <= 1e6)) usage_die("-secs", s);
	return v;
}

static int gz;
static struct libdeflate_compressor *ldc; static struct libdeflate_decompressor *ldd;
// Freed at every exit path (die included) so sanitizer runs are leak-clean.
static unsigned char *g_in, *g_z, *g_back; static size_t *g_zl; static FILE *g_f, *g_dumpf; static char *g_dumptmp;
static void cleanup(void) {
	if (g_f) fclose(g_f);
	if (g_dumpf) fclose(g_dumpf);
	if (g_dumptmp) { remove(g_dumptmp); free(g_dumptmp); }   // -dumpz failed before the rename
	free(g_in); free(g_z); free(g_back); free(g_zl);
	if (ldc) libdeflate_free_compressor(ldc);
	if (ldd) libdeflate_free_decompressor(ldd);
}

static size_t compress_one(const unsigned char *in, size_t n, unsigned char *out, size_t cap) {
	size_t r = gz ? libdeflate_gzip_compress(ldc, in, n, out, cap)
		      : libdeflate_deflate_compress(ldc, in, n, out, cap);
	if (!r) die("compress failed");
	return r;
}
static size_t decompress_one(const unsigned char *in, size_t n, unsigned char *out, size_t cap) {
	size_t r;
	if (gz ? libdeflate_gzip_decompress(ldd, in, n, out, cap, &r)
	       : libdeflate_deflate_decompress(ldd, in, n, out, cap, &r))
		die("decompress failed");
	return r;
}

int main(int argc, char **argv) {
	const char *file = NULL, *mode = "c", *dumpz = NULL; int mode_set = 0; double secs = 3; int level = 6; size_t block = 0;
	for (int i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "-level") && i + 1 < argc) level = (int)parse_num("-level", argv[++i], 0, 12);
		else if (!strcmp(argv[i], "-mode") && i + 1 < argc) mode = argv[++i], mode_set = 1;
		else if (!strcmp(argv[i], "-secs") && i + 1 < argc) secs = parse_secs(argv[++i]);
		else if (!strcmp(argv[i], "-block") && i + 1 < argc) block = (size_t)parse_num("-block", argv[++i], 1, 1L << 40);
		else if (!strcmp(argv[i], "-dumpz")) {
			if (i + 1 >= argc) usage_die("-dumpz", "(missing PATH)");
			dumpz = argv[++i];
		}
		else if (!strcmp(argv[i], "-format") && i + 1 < argc) {
			const char *f = argv[++i];
			if (!strcmp(f, "gzip")) gz = 1; else if (!strcmp(f, "raw")) gz = 0; else die("bad -format (raw|gzip)");
		} else file = argv[i];
	}
	if (!file) die("no input file");
	if (dumpz && mode_set) usage_die("-dumpz", "(cannot be combined with -mode; it always compresses)");
	atexit(cleanup);
	FILE *f = g_f = fopen(file, "rb"); if (!f) die("open");
	fseek(f, 0, SEEK_END); size_t n = ftell(f); fseek(f, 0, SEEK_SET);
	if (!block || block > n) block = n ? n : 1;
	size_t nb = n ? (n + block - 1) / block : 1;
	size_t bcap = block + block / 2 + 1024;           // per-block compressed capacity
	unsigned char *in = malloc(n + 1), *z = malloc(nb * bcap), *back = malloc(n + 1);
	size_t *zl = malloc(nb * sizeof *zl);
	g_in = in; g_z = z; g_back = back; g_zl = zl;
	if (!in || !z || !back || !zl || fread(in, 1, n, f) != n) die("read");
	fclose(f); g_f = NULL;
	ldc = libdeflate_alloc_compressor(level); ldd = libdeflate_alloc_decompressor();
	if (!ldc || !ldd) die("alloc");

	#define BLEN(b) ((b) + 1 == nb ? n - (b) * block : block)
	size_t zn = 0;
	for (size_t b = 0; b < nb; b++) { zl[b] = compress_one(in + b * block, BLEN(b), z + b * bcap, bcap); zn += zl[b]; }
	for (size_t b = 0; b < nb; b++)
		if (decompress_one(z + b * bcap, zl[b], back + b * block, BLEN(b)) != BLEN(b)) die("roundtrip mismatch");
	if (memcmp(in, back, n)) die("roundtrip mismatch");
	if (dumpz) {
		// Write to PATH.tmp.PID and rename on success, so a failed run never leaves a partial PATH.
		size_t tl = strlen(dumpz) + 32; g_dumptmp = malloc(tl); if (!g_dumptmp) die("alloc");
		snprintf(g_dumptmp, tl, "%s.tmp.%ld", dumpz, (long)getpid());
		FILE *o = g_dumpf = fopen(g_dumptmp, "wb"); if (!o) die("open -dumpz");
		for (size_t b = 0; b < nb; b++) {
			unsigned char h[8]; for (int k = 0; k < 8; k++) h[k] = (unsigned char)((uint64_t)zl[b] >> (8 * k));
			if (fwrite(h, 1, 8, o) != 8 || fwrite(z + b * bcap, 1, zl[b], o) != zl[b]) die("write -dumpz");
		}
		g_dumpf = NULL;
		if (fclose(o)) die("write -dumpz");
		if (rename(g_dumptmp, dumpz)) die("rename -dumpz");
		free(g_dumptmp); g_dumptmp = NULL;
		return 0;
	}
	int dec = mode[0] == 'd';

	double best = 0, total = 0; long loops = 1;
	while (total < secs) {
		double t0 = now();
		for (long i = 0; i < loops; i++)
			for (size_t b = 0; b < nb; b++) {
				if (dec) decompress_one(z + b * bcap, zl[b], back + b * block, BLEN(b));
				else compress_one(in + b * block, BLEN(b), z + b * bcap, bcap);
			}
		double dt = now() - t0; total += dt;
		if (dt >= 0.5) { double mbs = (double)n * loops / dt / 1e6; if (mbs > best) best = mbs; }
		loops = dt < 0.02 ? loops * 10 : (long)(loops / dt) + 1; // aim for ~1s windows
	}
	if (best == 0) die("no window >= 0.5s");
	printf("%.1f %.3f %zu\n", best, (double)n / zn, zn); // 3rd field: exact total compressed bytes
	if (fflush(stdout) || ferror(stdout)) die("write error");
	return 0;
}

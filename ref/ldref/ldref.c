/*
 * ldref - C reference driver for fastzstd's DEFLATE work. Built straight from
 * ref/libdeflate/lib (libdeflate v1.26) as ONE translation unit (see the
 * Makefile). Reads stdin, writes stdout. Exit status 0 on success, nonzero
 * (with a message on stderr) on any error.
 *
 * Common option: -format raw|gzip|zlib   container (default raw)
 *
 * SUBCOMMANDS
 *
 * compress -l N [-format F]
 *   N in 0..12. Compresses stdin with libdeflate_{deflate,gzip,zlib}_compress
 *   into a buffer sized by the matching *_compress_bound; writes the result.
 *
 * decompress [-format F] -out-size N [-exact 0|1]
 *   Decompresses stdin into an N-byte buffer and writes the output. On any
 *   libdeflate error, exits 10+result (11 bad_data, 12 short_output,
 *   13 insufficient_space) with a message on stderr and writes nothing.
 *
 * decompress-ex [-format F] -out-size N [-exact 0|1]
 *   Same, but for the four libdeflate results it ALWAYS exits 0 and writes, to
 *   stdout, a one-line header in this fixed format
 *       result=<R> in=<I> out=<O>\n
 *   followed by exactly O output bytes. R: 0 ok, 1 bad_data, 2 short_output,
 *   3 insufficient_space. I and O are the actual input bytes consumed and
 *   output bytes produced; they are 0 whenever R != 0 (libdeflate defines
 *   them only on success), and then no output bytes follow.
 *   -exact 0 (default): the actual-out pointer is passed, so any output of at
 *   most N bytes succeeds (short_output cannot happen).
 *   -exact 1: the actual-out pointer is NULL, so the data must decompress to
 *   exactly N bytes (fewer gives short_output, more insufficient_space).
 *
 * decompress-batch [-format F]
 *   Many decompress-ex calls in one process (T1.2 differential tests); the
 *   framing is documented above cmd_decompress_batch.
 *
 * version
 *   Prints "libdeflate v1.26" then "key=value" lines describing the selected
 *   implementation (best effort, see cmd_version).
 *
 * dump-tables -kind precode|litlen|offset|static
 *   Dumps the decompressor's Huffman decode tables (T1.1); the input and
 *   output framing is documented above cmd_dump_tables.
 *
 * dump-codes | dump-header | dump-static | dump-bits
 *   The compressor's Huffman code builder, dynamic-header precomputation,
 *   static codes + slot tables, and output bitstream macros (T2.1); framing
 *   documented above each cmd_dump_* function.
 *
 * dump-flush | compress-bound -l N | dump-params -l N
 *   The block writer (deflate_flush_block via deflate_finish_block, fed by
 *   deflate_begin_sequences / deflate_choose_*), the compress bound and the
 *   level table parameters (T2.2); framing documented above each function.
 *
 * compress-batch | dump-htmf
 *   Many raw libdeflate_deflate_compress calls with chosen output buffer
 *   sizes in one process, and the level 1 hash table matchfinder state after
 *   a compression (T2.3); framing documented above each function.
 *
 * dump-split | dump-minlen | dump-hcmf
 *   The block splitting statistics functions driven by a script, the
 *   min-match-length heuristic, and the hc_matchfinder (driven op by op, or
 *   its state after a level 2..9 compression) (T2.4); framing documented
 *   above each function.
 *
 * dump-btmf | dump-nopt
 *   The bt_matchfinder (driven op by op, or its state after a level 10..12
 *   compression), and the near-optimal parser's cost model, statistics,
 *   minimum-cost path and optimize-and-flush on one compressor's state,
 *   driven by a script (T2.6); framing documented above each function.
 *
 * Because the library is one translation unit, the dump-* subcommands can
 * reach libdeflate's static internals directly.
 */
#define _POSIX_C_SOURCE 200809L
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* The library, as one translation unit. The rest (utils.c, crc32.c, adler32.c,
 * the arch cpu_features.c) is linked as separate objects. The only clash
 * between these files is the macro BITBUF_NBITS, which deflate_decompress.c and
 * deflate_compress.c define differently (8*sizeof vs 8*sizeof-1); it is
 * #undef'd in between so each file sees its own. After this block BITBUF_NBITS
 * has the compressor's meaning (the decompressor's code is already compiled);
 * a dump-* subcommand needing the decompressor's value must redefine it. */
#include "deflate_decompress.c"
#undef BITBUF_NBITS
#include "deflate_compress.c"
#include "gzip_compress.c"
#include "gzip_decompress.c"
#include "zlib_compress.c"
#include "zlib_decompress.c"

enum { RAW, GZIP, ZLIB };

/* Everything allocated is released by cleanup(), registered with atexit(), so
 * every exit path (return from main, die, usage exit) frees its buffers and
 * stdio is flushed normally (LeakSanitizer-clean). */
static void *g_alloc[8];
static int g_nalloc;
static struct libdeflate_compressor *g_c;
static struct libdeflate_decompressor *g_d;

static void cleanup(void)
{
	if (g_c)
		libdeflate_free_compressor(g_c);
	if (g_d)
		libdeflate_free_decompressor(g_d);
	while (g_nalloc > 0)
		free(g_alloc[--g_nalloc]);
}

static void die(const char *m)
{
	fprintf(stderr, "ldref: %s\n", m);
	exit(1);
}

static void usage_die(const char *what, const char *arg)
{
	fprintf(stderr, "ldref: invalid value for %s: %s\n", what, arg);
	exit(2);
}

/* Parse a decimal integer in [lo, hi]; exits 2 on anything else. */
static long parse_num(const char *what, const char *s, long lo, long hi)
{
	char *end;
	long v;

	errno = 0;
	v = strtol(s, &end, 10);
	if (end == s || *end != '\0' || errno == ERANGE || v < lo || v > hi)
		usage_die(what, s);
	return v;
}

static void *track(void *p)
{
	if (g_nalloc >= (int)(sizeof(g_alloc) / sizeof(g_alloc[0]))) {
		free(p);
		die("internal: too many allocations");
	}
	return g_alloc[g_nalloc++] = p;
}

static void *xmalloc(size_t n)
{
	void *p = malloc(n ? n : 1);

	if (!p)
		die("out of memory");
	return track(p);
}

static unsigned char *read_all(FILE *f, size_t *n)
{
	size_t cap = 1 << 16, len = 0, r;
	unsigned char *b = malloc(cap);

	if (!b)
		die("out of memory");
	while ((r = fread(b + len, 1, cap - len, f)) > 0) {
		len += r;
		if (len == cap) {
			cap *= 2;
			void *nb = realloc(b, cap);

			if (!nb) {
				free(b);
				die("out of memory");
			}
			b = nb;
		}
	}
	if (ferror(f)) {
		free(b);
		die("read error");
	}
	*n = len;
	return track(b);
}

static void write_all(const void *p, size_t n)
{
	if (n && fwrite(p, 1, n, stdout) != n)
		die("write error");
}

static int parse_format(const char *s)
{
	if (!strcmp(s, "raw"))
		return RAW;
	if (!strcmp(s, "gzip"))
		return GZIP;
	if (!strcmp(s, "zlib"))
		return ZLIB;
	die("bad -format (raw|gzip|zlib)");
	return 0;
}

struct opts {
	int level;
	int format;
	long out_size;
	int exact;
};

static void parse_opts(int argc, char **argv, struct opts *o)
{
	memset(o, 0, sizeof(*o));
	o->level = -1;
	o->out_size = -1;
	for (int i = 2; i < argc; i++) {
		if (!strcmp(argv[i], "-l") && i + 1 < argc)
			o->level = (int)parse_num("-l", argv[i + 1], 0, 12), i++;
		else if (!strcmp(argv[i], "-format") && i + 1 < argc)
			o->format = parse_format(argv[++i]);
		else if (!strcmp(argv[i], "-out-size") && i + 1 < argc)
			o->out_size = parse_num("-out-size", argv[i + 1], 0, 1L << 40), i++;
		else if (!strcmp(argv[i], "-exact") && i + 1 < argc)
			o->exact = (int)parse_num("-exact", argv[i + 1], 0, 1), i++;
		else {
			fprintf(stderr, "ldref: unknown option %s\n", argv[i]);
			exit(2);
		}
	}
}

static int cmd_compress(int argc, char **argv)
{
	struct opts o;
	size_t n, bound, r;
	unsigned char *in, *out;
	struct libdeflate_compressor *c;

	parse_opts(argc, argv, &o);
	if (o.level < 0 || o.level > 12)
		die("compress: -l N (0..12) required");
	in = read_all(stdin, &n);
	c = g_c = libdeflate_alloc_compressor(o.level);
	if (!c)
		die("alloc_compressor failed");
	switch (o.format) {
	case GZIP: bound = libdeflate_gzip_compress_bound(c, n); break;
	case ZLIB: bound = libdeflate_zlib_compress_bound(c, n); break;
	default:   bound = libdeflate_deflate_compress_bound(c, n); break;
	}
	out = xmalloc(bound);
	switch (o.format) {
	case GZIP: r = libdeflate_gzip_compress(c, in, n, out, bound); break;
	case ZLIB: r = libdeflate_zlib_compress(c, in, n, out, bound); break;
	default:   r = libdeflate_deflate_compress(c, in, n, out, bound); break;
	}
	if (r == 0)
		die("compress failed (output did not fit the bound)");
	write_all(out, r);
	return 0;
}

/* Shared by decompress and decompress-ex. */
static enum libdeflate_result do_decompress(const struct opts *o,
					    const unsigned char *in, size_t n,
					    unsigned char *out,
					    size_t *act_in, size_t *act_out)
{
	struct libdeflate_decompressor *d = g_d = libdeflate_alloc_decompressor();
	size_t cap = (size_t)o->out_size;
	size_t *ao = o->exact ? NULL : act_out;
	enum libdeflate_result res;

	if (!d)
		die("alloc_decompressor failed");
	switch (o->format) {
	case GZIP: res = libdeflate_gzip_decompress_ex(d, in, n, out, cap, act_in, ao); break;
	case ZLIB: res = libdeflate_zlib_decompress_ex(d, in, n, out, cap, act_in, ao); break;
	default:   res = libdeflate_deflate_decompress_ex(d, in, n, out, cap, act_in, ao); break;
	}
	if (res == LIBDEFLATE_SUCCESS && o->exact)
		*act_out = cap;
	return res;
}

static int decompress_common(int argc, char **argv, int ex)
{
	static const char *const names[] = {
		"ok", "bad_data", "short_output", "insufficient_space"
	};
	struct opts o;
	size_t n, act_in = 0, act_out = 0;
	unsigned char *in, *out;
	enum libdeflate_result res;

	parse_opts(argc, argv, &o);
	if (o.out_size < 0)
		die("decompress: -out-size N required");
	in = read_all(stdin, &n);
	out = xmalloc((size_t)o.out_size);
	res = do_decompress(&o, in, n, out, &act_in, &act_out);
	if (res != LIBDEFLATE_SUCCESS) {
		act_in = act_out = 0;
		if (!ex) {
			fprintf(stderr, "ldref: decompress: %s\n", names[res]);
			return 10 + (int)res;
		}
	}
	if (ex)
		printf("result=%d in=%zu out=%zu\n", (int)res, act_in, act_out);
	write_all(out, act_out);
	return 0;
}

static int cmd_decompress(int argc, char **argv)
{
	return decompress_common(argc, argv, 0);
}

static int cmd_decompress_ex(int argc, char **argv)
{
	return decompress_common(argc, argv, 1);
}

static uint64_t le64(const unsigned char *p)
{
	uint64_t v = 0;

	for (int i = 7; i >= 0; i--)
		v = (v << 8) | p[i];
	return v;
}

/*
 * decompress-batch: stdin is a sequence of records until EOF, each
 *     u8 exact (0|1), u64 LE out_size, u64 LE in_len, in_len input bytes;
 * for each record, in order, stdout gets exactly what `decompress-ex
 * -exact <exact> -out-size <out_size>` would print for that input (the
 * header line, then the output bytes on success). One decompressor (from
 * libdeflate_alloc_decompressor) is reused for all records, as a pooled one
 * would be; the output buffer is one allocation of the largest out_size.
 * Malformed framing exits 1.
 */
static int cmd_decompress_batch(int argc, char **argv)
{
	struct opts o;
	size_t n, pos, maxout = 0;
	unsigned char *in, *out;
	struct libdeflate_decompressor *d;

	parse_opts(argc, argv, &o);
	in = read_all(stdin, &n);
	for (pos = 0; pos < n;) {
		uint64_t osz, len;

		if (n - pos < 17)
			die("decompress-batch: truncated record header");
		if (in[pos] > 1)
			die("decompress-batch: bad exact flag");
		osz = le64(in + pos + 1);
		len = le64(in + pos + 9);
		pos += 17;
		if (osz > ((uint64_t)1 << 40) || len > n - pos)
			die("decompress-batch: bad record sizes");
		if (osz > maxout)
			maxout = (size_t)osz;
		pos += (size_t)len;
	}
	out = xmalloc(maxout);
	d = g_d = libdeflate_alloc_decompressor();
	if (!d)
		die("alloc_decompressor failed");
	for (pos = 0; pos < n;) {
		int exact = in[pos];
		size_t cap = (size_t)le64(in + pos + 1);
		size_t len = (size_t)le64(in + pos + 9);
		const unsigned char *p = in + pos + 17;
		size_t act_in = 0, act_out = 0;
		size_t *ao = exact ? NULL : &act_out;
		enum libdeflate_result res;

		pos += 17 + len;
		switch (o.format) {
		case GZIP: res = libdeflate_gzip_decompress_ex(d, p, len, out, cap, &act_in, ao); break;
		case ZLIB: res = libdeflate_zlib_decompress_ex(d, p, len, out, cap, &act_in, ao); break;
		default:   res = libdeflate_deflate_decompress_ex(d, p, len, out, cap, &act_in, ao); break;
		}
		if (res == LIBDEFLATE_SUCCESS && exact)
			act_out = cap;
		if (res != LIBDEFLATE_SUCCESS)
			act_in = act_out = 0;
		printf("result=%d in=%zu out=%zu\n", (int)res, act_in, act_out);
		write_all(out, act_out);
	}
	return 0;
}

/*
 * Best effort: the BMI2 decompressor is selected at run time by libdeflate
 * (deflate_decompress_bmi2 when the CPU reports BMI2, else the _default one),
 * so we report what its dispatcher will choose from the same feature word.
 * The x86 target attributes are per-function in lib/x86, so no -m flags
 * are needed to compile the BMI2 variant in.
 */
static int cmd_version(int argc, char **argv)
{
	(void)argc; (void)argv;
	printf("libdeflate v%s\n", LIBDEFLATE_VERSION_STRING);
#if defined(ARCH_X86_32) || defined(ARCH_X86_64)
	{
		u32 f = get_x86_cpu_features();

		printf("arch=x86\n");
		printf("cpu_features=0x%x\n", (unsigned)f);
#ifdef deflate_decompress_bmi2
		printf("decompress=%s\n", HAVE_BMI2(f) ? "bmi2" : "default");
#else
		printf("decompress=default\n");
#endif
	}
#else
	printf("arch=other\ndecompress=default\n");
#endif
	return 0;
}

/*
 * dump-tables -kind precode|litlen|offset|static
 *
 * Calls the decompressor's static table builders (build_precode_decode_table,
 * build_offset_decode_table, build_litlen_decode_table) on a decompressor from
 * libdeflate_alloc_decompressor(); everything but free_func is memset to 0
 * again before every record (the state libdeflate_alloc_decompressor_ex
 * leaves it in), so records are independent. The lens are placed where the
 * decoder puts them (d->u.precode_lens, d->u.l.lens), so the union residue in
 * the litlen table (entries the builder does not write still hold the lens
 * bytes) is exactly what the decoder would leave there.
 *
 * Input (stdin), a sequence of records until EOF:
 *   precode: 19 bytes per record, each a codeword length 0..7, in symbol
 *            order (not the permuted transmission order).
 *   litlen or offset (same framing and output): u16 LE num_litlen_syms
 *            (0..288), that many litlen lengths 0..15, u16 LE num_offset_syms
 *            (0..32), that many offset lengths 0..15. The lengths are stored
 *            contiguously in d->u.l.lens as in a dynamic block; the offset
 *            table is built first, then the litlen table (the decoder's
 *            order: the litlen build overwrites lens), and BOTH tables are
 *            output, litlen first.
 *   static:  no input (stdin is not read); one record with the static code
 *            lengths set exactly as decompress_template.h sets them.
 * A malformed record (bad count, length too large, truncated) exits 1; the
 * output of the records before it has already been written and is still
 * flushed by exit() (callers must treat a nonzero exit as failure of the
 * whole run, as creftest.LDRun does).
 *
 * Output (stdout), per table built:
 *   one line "ok=<0|1> kind=<k>\n" (k = precode, litlen or offset; for the
 *   litlen table the line is "ok=<0|1> kind=litlen tablebits=<n>\n", n being
 *   d->litlen_tablebits after the call), then the whole fixed-size table
 *   array of the struct as raw little-endian u32 entries: PRECODE_ENOUGH,
 *   LITLEN_ENOUGH or OFFSET_ENOUGH entries. ok is the builder's return value.
 *   Records are output in input order.
 */
static void dt_put_table(const char *kind, bool ok, const u32 *t, size_t n,
			 int tablebits)
{
	unsigned char b[4];

	if (tablebits >= 0)
		printf("ok=%d kind=%s tablebits=%d\n", ok ? 1 : 0, kind, tablebits);
	else
		printf("ok=%d kind=%s\n", ok ? 1 : 0, kind);
	for (size_t i = 0; i < n; i++) {
		b[0] = (unsigned char)t[i];
		b[1] = (unsigned char)(t[i] >> 8);
		b[2] = (unsigned char)(t[i] >> 16);
		b[3] = (unsigned char)(t[i] >> 24);
		write_all(b, 4);
	}
}

/* Offset table first, then litlen (the decoder's order); litlen output first. */
static void dt_build_litlen_offset(struct libdeflate_decompressor *d,
				   unsigned nl, unsigned no)
{
	bool ok_off = build_offset_decode_table(d, nl, no);
	bool ok_lit = build_litlen_decode_table(d, nl, no);

	dt_put_table("litlen", ok_lit, d->u.litlen_decode_table, LITLEN_ENOUGH,
		     (int)d->litlen_tablebits);
	dt_put_table("offset", ok_off, d->offset_decode_table, OFFSET_ENOUGH, -1);
}

static void dt_reset(struct libdeflate_decompressor *d)
{
	memset(d, 0, offsetof(struct libdeflate_decompressor, free_func));
}

static unsigned dt_u16(const unsigned char *p)
{
	return (unsigned)p[0] | ((unsigned)p[1] << 8);
}

static int cmd_dump_tables(int argc, char **argv)
{
	const char *kind = NULL;
	struct libdeflate_decompressor *d;
	unsigned char *in;
	size_t n, pos = 0;

	for (int i = 2; i < argc; i++) {
		if (!strcmp(argv[i], "-kind") && i + 1 < argc)
			kind = argv[++i];
		else {
			fprintf(stderr, "ldref: unknown option %s\n", argv[i]);
			exit(2);
		}
	}
	if (!kind || (strcmp(kind, "precode") && strcmp(kind, "litlen") &&
		      strcmp(kind, "offset") && strcmp(kind, "static")))
		die("dump-tables: -kind precode|litlen|offset|static required");
	d = g_d = libdeflate_alloc_decompressor();
	if (!d)
		die("alloc_decompressor failed");

	if (!strcmp(kind, "static")) {
		unsigned i;

		dt_reset(d);
		/* As in decompress_template.h (static Huffman block). */
		for (i = 0; i < 144; i++)
			d->u.l.lens[i] = 8;
		for (; i < 256; i++)
			d->u.l.lens[i] = 9;
		for (; i < 280; i++)
			d->u.l.lens[i] = 7;
		for (; i < 288; i++)
			d->u.l.lens[i] = 8;
		for (; i < 288 + 32; i++)
			d->u.l.lens[i] = 5;
		dt_build_litlen_offset(d, 288, 32);
		return 0;
	}

	in = read_all(stdin, &n);
	if (!strcmp(kind, "precode")) {
		if (n % DEFLATE_NUM_PRECODE_SYMS)
			die("dump-tables: precode input is not a multiple of 19 bytes");
		for (; pos < n; pos += DEFLATE_NUM_PRECODE_SYMS) {
			bool ok;

			dt_reset(d);
			for (unsigned i = 0; i < DEFLATE_NUM_PRECODE_SYMS; i++) {
				if (in[pos + i] > DEFLATE_MAX_PRE_CODEWORD_LEN)
					die("dump-tables: precode length > 7");
				d->u.precode_lens[i] = in[pos + i];
			}
			ok = build_precode_decode_table(d);
			dt_put_table("precode", ok, d->u.l.precode_decode_table,
				     PRECODE_ENOUGH, -1);
		}
		return 0;
	}

	/* litlen / offset */
	while (pos < n) {
		unsigned nl, no;

		dt_reset(d);
		if (n - pos < 2)
			die("dump-tables: truncated record");
		nl = dt_u16(in + pos);
		pos += 2;
		if (nl > DEFLATE_NUM_LITLEN_SYMS || n - pos < nl)
			die("dump-tables: bad num_litlen_syms or truncated record");
		for (unsigned i = 0; i < nl; i++) {
			if (in[pos + i] > DEFLATE_MAX_LITLEN_CODEWORD_LEN)
				die("dump-tables: litlen length > 15");
			d->u.l.lens[i] = in[pos + i];
		}
		pos += nl;
		if (n - pos < 2)
			die("dump-tables: truncated record");
		no = dt_u16(in + pos);
		pos += 2;
		if (no > DEFLATE_NUM_OFFSET_SYMS || n - pos < no)
			die("dump-tables: bad num_offset_syms or truncated record");
		for (unsigned i = 0; i < no; i++) {
			if (in[pos + i] > DEFLATE_MAX_OFFSET_CODEWORD_LEN)
				die("dump-tables: offset length > 15");
			d->u.l.lens[nl + i] = in[pos + i];
		}
		pos += no;
		dt_build_litlen_offset(d, nl, no);
	}
	return 0;
}

/* ---- compressor dumps (T2.1) ---------------------------------------------- */

static u32 le32(const unsigned char *p)
{
	return (u32)p[0] | ((u32)p[1] << 8) | ((u32)p[2] << 16) |
	       ((u32)p[3] << 24);
}

static void put_u32s(const u32 *v, size_t n)
{
	unsigned char b[4];

	for (size_t i = 0; i < n; i++) {
		b[0] = (unsigned char)v[i];
		b[1] = (unsigned char)(v[i] >> 8);
		b[2] = (unsigned char)(v[i] >> 16);
		b[3] = (unsigned char)(v[i] >> 24);
		write_all(b, 4);
	}
}

/* Compressor for the dump-* commands below (level 12: offset_slot_full is
 * initialized; c->freqs, c->codes and c->o are reset per record where used). */
static struct libdeflate_compressor *dc_compressor(void)
{
	if (!g_c)
		g_c = libdeflate_alloc_compressor(12);
	if (!g_c)
		die("alloc_compressor failed");
	return g_c;
}

/*
 * dump-codes: the compressor's Huffman code builder.
 *
 * Input (stdin), records until EOF, each:
 *   u8 kind, u16 LE n, then n u32 LE frequencies.
 *   kind 0: deflate_make_huffman_code(n, MAX_LITLEN_CODEWORD_LEN (14), ...),
 *           2 <= n <= 288 (the litlen limit is 14, not the format's 15: that
 *           is what deflate_make_huffman_codes uses);
 *   kind 1: deflate_make_huffman_code(n, MAX_OFFSET_CODEWORD_LEN (15), ...),
 *           2 <= n <= 32;
 *   kind 2: deflate_make_huffman_code(n, MAX_PRE_CODEWORD_LEN (7), ...),
 *           2 <= n <= 19;
 *   kind 3: n must be 320: litlen freqs (288) then offset freqs (32), copied
 *           into c->freqs; calls deflate_make_huffman_codes(&c->freqs,
 *           &c->codes) then deflate_compute_full_len_codewords(c, &c->codes).
 *   For n == 2 at least one frequency must be nonzero: with both zero C's
 *   sort_symbols wraps its counter arithmetic and heap_sort runs off the
 *   array (C only ever uses n = 19, 32, 288), so such a record exits 1.
 *   Frequencies are arbitrary u32 (sums >= 2^22 wrap in C's packed
 *   representation; that is well defined u32 arithmetic and reproduced).
 * Before each record the lens/codewords arrays (kinds 0..2: local arrays of
 * 288 entries; kind 3: c->freqs, c->codes and c->o) are zeroed, so entries
 * the builder leaves unwritten read as 0.
 * Output per record: n lens bytes, then n codewords as u32 LE (kind 3: the
 * 320 bytes of c->codes.lens, the 288 + 32 codewords, then
 * c->o.length.codewords[0..258] as u32 LE and c->o.length.lens[0..258]).
 * Malformed input exits 1 (earlier records' output is still flushed).
 */
static int cmd_dump_codes(int argc, char **argv)
{
	static u32 freqs[DEFLATE_MAX_NUM_SYMS], codewords[DEFLATE_MAX_NUM_SYMS];
	static u8 lens[DEFLATE_MAX_NUM_SYMS];
	static const unsigned maxn[3] = { DEFLATE_NUM_LITLEN_SYMS,
					  DEFLATE_NUM_OFFSET_SYMS,
					  DEFLATE_NUM_PRECODE_SYMS };
	static const unsigned maxlen[3] = { MAX_LITLEN_CODEWORD_LEN,
					    MAX_OFFSET_CODEWORD_LEN,
					    MAX_PRE_CODEWORD_LEN };
	unsigned char *in;
	size_t n, pos = 0;

	(void)argv;
	if (argc != 2)
		die("dump-codes takes no options");
	in = read_all(stdin, &n);
	while (pos < n) {
		unsigned kind, ns, i;
		u32 any = 0;

		if (n - pos < 3)
			die("dump-codes: truncated record header");
		kind = in[pos];
		ns = dt_u16(in + pos + 1);
		pos += 3;
		if (kind > 3 || (kind == 3 && ns != 320) ||
		    (kind < 3 && (ns < 2 || ns > maxn[kind])))
			die("dump-codes: bad kind or n");
		if ((n - pos) / 4 < ns)
			die("dump-codes: truncated frequencies");
		for (i = 0; i < ns; i++)
			any |= le32(in + pos + 4 * i);
		if (ns == 2 && !any)
			die("dump-codes: n == 2 with all frequencies 0 (undefined in C)");
		if (kind == 3) {
			struct libdeflate_compressor *c = dc_compressor();

			memset(&c->freqs, 0, sizeof(c->freqs));
			memset(&c->codes, 0, sizeof(c->codes));
			memset(&c->o, 0, sizeof(c->o));
			for (i = 0; i < DEFLATE_NUM_LITLEN_SYMS; i++)
				c->freqs.litlen[i] = le32(in + pos + 4 * i);
			for (i = 0; i < DEFLATE_NUM_OFFSET_SYMS; i++)
				c->freqs.offset[i] = le32(in + pos + 4 *
					(DEFLATE_NUM_LITLEN_SYMS + i));
			pos += 4 * ns;
			deflate_make_huffman_codes(&c->freqs, &c->codes);
			deflate_compute_full_len_codewords(c, &c->codes);
			write_all(&c->codes.lens, sizeof(c->codes.lens));
			put_u32s(c->codes.codewords.litlen, DEFLATE_NUM_LITLEN_SYMS);
			put_u32s(c->codes.codewords.offset, DEFLATE_NUM_OFFSET_SYMS);
			put_u32s(c->o.length.codewords, DEFLATE_MAX_MATCH_LEN + 1);
			write_all(c->o.length.lens, DEFLATE_MAX_MATCH_LEN + 1);
			continue;
		}
		memset(freqs, 0, sizeof(freqs));
		memset(codewords, 0, sizeof(codewords));
		memset(lens, 0, sizeof(lens));
		for (i = 0; i < ns; i++)
			freqs[i] = le32(in + pos + 4 * i);
		pos += 4 * ns;
		deflate_make_huffman_code(ns, maxlen[kind], freqs, lens, codewords);
		write_all(lens, ns);
		put_u32s(codewords, ns);
	}
	return 0;
}

/*
 * dump-header: deflate_precompute_huffman_header.
 *
 * Input, records until EOF, each: u16 LE nl (0..288), nl litlen codeword
 * lengths (0..15), u16 LE no (0..32), no offset codeword lengths (0..15).
 * Per record c->codes and c->o are zeroed, the lengths are stored in
 * c->codes.lens.litlen[0..nl) and c->codes.lens.offset[0..no) (the rest stays
 * 0) and deflate_precompute_huffman_header(c) is called.
 * Output per record: a line
 *   "nl=<num_litlen_syms> no=<num_offset_syms> nexp=<num_explicit_lens>
 *    nitems=<num_items>\n"   (one line, single spaces)
 * then c->o.precode.freqs (19 u32 LE), .lens (19 bytes), .codewords (19 u32
 * LE), .items[0..num_items) (u32 LE) and finally the 320 bytes of
 * c->codes.lens after the call (the function leaves a copy of the offset
 * lengths in the unused litlen slots when nl < 288).
 * Malformed input exits 1.
 */
static int cmd_dump_header(int argc, char **argv)
{
	struct libdeflate_compressor *c;
	unsigned char *in;
	size_t n, pos = 0;

	(void)argv;
	if (argc != 2)
		die("dump-header takes no options");
	in = read_all(stdin, &n);
	c = dc_compressor();
	while (pos < n) {
		unsigned nl, no, i;

		memset(&c->codes, 0, sizeof(c->codes));
		memset(&c->o, 0, sizeof(c->o));
		if (n - pos < 2)
			die("dump-header: truncated record");
		nl = dt_u16(in + pos);
		pos += 2;
		if (nl > DEFLATE_NUM_LITLEN_SYMS || n - pos < nl)
			die("dump-header: bad nl or truncated record");
		for (i = 0; i < nl; i++) {
			if (in[pos + i] > DEFLATE_MAX_CODEWORD_LEN)
				die("dump-header: length > 15");
			c->codes.lens.litlen[i] = in[pos + i];
		}
		pos += nl;
		if (n - pos < 2)
			die("dump-header: truncated record");
		no = dt_u16(in + pos);
		pos += 2;
		if (no > DEFLATE_NUM_OFFSET_SYMS || n - pos < no)
			die("dump-header: bad no or truncated record");
		for (i = 0; i < no; i++) {
			if (in[pos + i] > DEFLATE_MAX_CODEWORD_LEN)
				die("dump-header: length > 15");
			c->codes.lens.offset[i] = in[pos + i];
		}
		pos += no;
		deflate_precompute_huffman_header(c);
		printf("nl=%u no=%u nexp=%u nitems=%u\n",
		       c->o.precode.num_litlen_syms,
		       c->o.precode.num_offset_syms,
		       c->o.precode.num_explicit_lens,
		       c->o.precode.num_items);
		put_u32s(c->o.precode.freqs, DEFLATE_NUM_PRECODE_SYMS);
		write_all(c->o.precode.lens, DEFLATE_NUM_PRECODE_SYMS);
		put_u32s(c->o.precode.codewords, DEFLATE_NUM_PRECODE_SYMS);
		for (i = 0; i < c->o.precode.num_items; i++) {
			u32 v = c->o.precode.items[i];

			put_u32s(&v, 1);
		}
		write_all(&c->codes.lens, sizeof(c->codes.lens));
	}
	return 0;
}

/*
 * dump-static: no input. Output, all binary, in this order:
 *   c->static_codes.lens (320 bytes), c->static_codes.codewords (288 + 32 u32
 *   LE), c->freqs as left by deflate_init_static_codes (288 + 32 u32 LE);
 *   deflate_length_slot_base (29 u32), deflate_extra_length_bits (29 bytes),
 *   deflate_offset_slot_base (30 u32), deflate_extra_offset_bits (30 bytes),
 *   deflate_length_slot (259 bytes), deflate_offset_slot (256 bytes),
 *   deflate_extra_precode_bits (19 bytes), deflate_precode_lens_permutation
 *   (19 bytes);
 *   c->p.n.offset_slot_full[1..32768] (32768 bytes; entry 0 is never
 *   written by deflate_init_offset_slot_full and is not output);
 *   deflate_get_offset_slot(offset) for offset 1..32768 (one byte each);
 *   after deflate_compute_full_len_codewords(c, &c->static_codes):
 *   c->o.length.codewords[3..258] (u32 LE) and c->o.length.lens[3..258].
 * The compressor is a fresh libdeflate_alloc_compressor(12).
 */
static int cmd_dump_static(int argc, char **argv)
{
	struct libdeflate_compressor *c;

	(void)argv;
	if (argc != 2)
		die("dump-static takes no options");
	c = dc_compressor();
	write_all(&c->static_codes.lens, sizeof(c->static_codes.lens));
	put_u32s(c->static_codes.codewords.litlen, DEFLATE_NUM_LITLEN_SYMS);
	put_u32s(c->static_codes.codewords.offset, DEFLATE_NUM_OFFSET_SYMS);
	put_u32s(c->freqs.litlen, DEFLATE_NUM_LITLEN_SYMS);
	put_u32s(c->freqs.offset, DEFLATE_NUM_OFFSET_SYMS);
	put_u32s(deflate_length_slot_base, ARRAY_LEN(deflate_length_slot_base));
	write_all(deflate_extra_length_bits, sizeof(deflate_extra_length_bits));
	put_u32s(deflate_offset_slot_base, ARRAY_LEN(deflate_offset_slot_base));
	write_all(deflate_extra_offset_bits, sizeof(deflate_extra_offset_bits));
	write_all(deflate_length_slot, sizeof(deflate_length_slot));
	write_all(deflate_offset_slot, sizeof(deflate_offset_slot));
	write_all(deflate_extra_precode_bits, sizeof(deflate_extra_precode_bits));
	write_all(deflate_precode_lens_permutation,
		  sizeof(deflate_precode_lens_permutation));
	write_all(&c->p.n.offset_slot_full[1], DEFLATE_MAX_MATCH_OFFSET);
	for (u32 off = 1; off <= DEFLATE_MAX_MATCH_OFFSET; off++) {
		unsigned char s = (unsigned char)deflate_get_offset_slot(off);

		write_all(&s, 1);
	}
	deflate_compute_full_len_codewords(c, &c->static_codes);
	put_u32s(&c->o.length.codewords[DEFLATE_MIN_MATCH_LEN],
		 DEFLATE_MAX_MATCH_LEN + 1 - DEFLATE_MIN_MATCH_LEN);
	write_all(&c->o.length.lens[DEFLATE_MIN_MATCH_LEN],
		  DEFLATE_MAX_MATCH_LEN + 1 - DEFLATE_MIN_MATCH_LEN);
	return 0;
}

/*
 * dump-bits: runs the compressor's real ADD_BITS / FLUSH_BITS macros (they
 * work on locals named bitbuf, bitcount, out_next, out_fast_end and os, as
 * in deflate_flush_block) over a script.
 *
 * Input: u32 LE cap (output buffer size, <= 1 MiB), u32 LE start (initial
 * out_next index, <= cap), u64 LE initial bitbuf, u8 initial bitcount
 * (<= BITBUF_NBITS), then ops until EOF:
 *   0, u8 n (0..32), u32 LE bits: ADD_BITS(bits, n); needs bitcount + n <=
 *      BITBUF_NBITS (else exit 1);
 *   1: FLUSH_BITS(); needs out_next + bitcount / 8 <= os->end (else exit 1:
 *      in C that is the caller's precondition);
 *   2: "new block": store the locals into *os and reload them, recomputing
 *      out_fast_end = os->end - MIN(WORDBYTES - 1, os->end - out_next)
 *      exactly as deflate_flush_block does on entry.
 * Before the script the buffer is filled with 0xA5 and out_fast_end is
 * computed as in op 2. Output: the cap buffer bytes, then the line
 *   "next=<out_next index> bitcount=<bitcount> bitbuf=<bitbuf, %016llx>\n".
 */
static int cmd_dump_bits(int argc, char **argv)
{
	struct deflate_output_bitstream os_s, *os = &os_s;
	unsigned char *in, *buf;
	size_t n, pos;
	u32 cap, start;
	bitbuf_t bitbuf;
	unsigned bitcount;
	u8 *out_next, *out_fast_end;

	(void)argv;
	if (argc != 2)
		die("dump-bits takes no options");
	in = read_all(stdin, &n);
	if (n < 17)
		die("dump-bits: truncated header");
	cap = le32(in);
	start = le32(in + 4);
	if (cap > (1u << 20) || start > cap || in[16] > BITBUF_NBITS)
		die("dump-bits: bad header");
	buf = xmalloc(cap);
	memset(buf, 0xA5, cap);
	os->bitbuf = (bitbuf_t)le64(in + 8);
	os->bitcount = in[16];
	os->next = buf + start;
	os->end = buf + cap;
	os->overflow = false;
	pos = 17;

#define DB_LOAD()							\
	do {								\
		bitbuf = os->bitbuf;					\
		bitcount = os->bitcount;				\
		out_next = os->next;					\
		out_fast_end = os->end -				\
			MIN(WORDBYTES - 1, os->end - out_next);		\
	} while (0)
	DB_LOAD();
	while (pos < n) {
		switch (in[pos]) {
		case 0: {
			unsigned nb;
			u32 bits;

			if (n - pos < 6)
				die("dump-bits: truncated add");
			nb = in[pos + 1];
			bits = le32(in + pos + 2);
			pos += 6;
			if (nb > 32 || bitcount + nb > BITBUF_NBITS)
				die("dump-bits: add exceeds BITBUF_NBITS");
			ADD_BITS(bits, nb);
			break;
		}
		case 1:
			pos++;
			if ((size_t)(os->end - out_next) < bitcount / 8)
				die("dump-bits: flush beyond the buffer end");
			FLUSH_BITS();
			break;
		case 2:
			pos++;
			os->bitbuf = bitbuf;
			os->bitcount = bitcount;
			os->next = out_next;
			DB_LOAD();
			break;
		default:
			die("dump-bits: bad op");
		}
	}
#undef DB_LOAD
	(void)out_fast_end;
	write_all(buf, cap);
	printf("next=%zu bitcount=%u bitbuf=%016llx\n",
	       (size_t)(out_next - buf), bitcount, (unsigned long long)bitbuf);
	return 0;
}

/* ---- block writer, level table, bound (T2.2) ----------------------------- */

static u16 le16(const unsigned char *p)
{
	return (u16)(p[0] | (p[1] << 8));
}

/* (Re)allocates g_c at level unless it already has it. */
static struct libdeflate_compressor *level_compressor(int level)
{
	if (g_c && (int)g_c->compression_level == level)
		return g_c;
	if (g_c)
		libdeflate_free_compressor(g_c);
	g_c = libdeflate_alloc_compressor(level);
	if (!g_c)
		die("alloc_compressor failed");
	return g_c;
}

#define DF_MAX_CAP	(8u << 20)

/*
 * dump-flush: the real deflate_finish_block / deflate_flush_block (block type
 * choice, space check, uncompressed/static/dynamic writers, WRITE_MATCH) fed
 * by the real deflate_begin_sequences / deflate_choose_literal /
 * deflate_choose_match (gather_split_stats = false), on a compressor of the
 * given level, so the static functions see the same state as in C.
 *
 * Input (stdin), records until EOF, each:
 *   u8 level (0..12), u32 LE cap (output buffer size, <= 8 MiB),
 *   u32 LE start (initial out_next index, <= cap), u8 bitcount (0..7),
 *   u8 bitbuf (no bits set at or above bitcount), u32 LE nblocks, then
 *   nblocks blocks, each:
 *     u8 final (0/1), u32 LE L (block length, 1..MAX_BLOCK_LENGTH), L data
 *     bytes, u32 LE ntokens, ntokens x (u16 LE length, u16 LE offset):
 *     length 0 = a literal (the next data byte; offset must be 0), else a
 *     match of length 3..258 and offset 1..32768 (the match bytes are
 *     skipped, never read). The tokens must consume exactly L bytes.
 * Per block: deflate_begin_sequences(c, seqs); one deflate_choose_literal /
 * deflate_choose_match per token; deflate_finish_block(c, os, data, L, seqs,
 * final). As in the compress loops, once os->overflow is set the remaining
 * blocks of the record are parsed but not flushed. No final partial byte is
 * written (that is libdeflate_deflate_compress's job).
 * The buffer is filled with 0xA5 before each record. Output per record: the
 * cap buffer bytes, then the line
 *   "next=<index> bitcount=<n> bitbuf=<%016llx> overflow=<0|1>\n".
 * Malformed input exits 1 (earlier records' output is still flushed).
 */
static int cmd_dump_flush(int argc, char **argv)
{
	unsigned char *in, *buf = NULL;
	struct deflate_sequence *seqs = NULL;
	size_t n, pos = 0;

	(void)argv;
	if (argc != 2)
		die("dump-flush takes no options");
	in = read_all(stdin, &n);
	while (pos < n) {
		struct deflate_output_bitstream os;
		struct libdeflate_compressor *c;
		u32 cap, start, nblocks;
		unsigned level, bitcount, bitbuf;

		if (n - pos < 15)
			die("dump-flush: truncated record header");
		level = in[pos];
		cap = le32(in + pos + 1);
		start = le32(in + pos + 5);
		bitcount = in[pos + 9];
		bitbuf = in[pos + 10];
		nblocks = le32(in + pos + 11);
		pos += 15;
		if (level > 12 || cap > DF_MAX_CAP || start > cap || bitcount > 7 ||
		    (bitbuf >> bitcount) != 0)
			die("dump-flush: bad record header");
		c = level_compressor((int)level);
		if (!buf) {
			buf = xmalloc(DF_MAX_CAP);
			seqs = xmalloc(sizeof(*seqs) * (MAX_BLOCK_LENGTH + 1));
		}
		memset(buf, 0xA5, cap);
		os.bitbuf = bitbuf;
		os.bitcount = bitcount;
		os.next = buf + start;
		os.end = buf + cap;
		os.overflow = false;

		for (u32 b = 0; b < nblocks; b++) {
			const unsigned char *data;
			struct deflate_sequence *seq = seqs;
			u32 len, ntok, used = 0;
			bool final;

			if (n - pos < 5)
				die("dump-flush: truncated block header");
			if (in[pos] > 1)
				die("dump-flush: bad final flag");
			final = in[pos] != 0;
			len = le32(in + pos + 1);
			pos += 5;
			if (len < 1 || len > MAX_BLOCK_LENGTH || n - pos < len)
				die("dump-flush: bad block length");
			data = in + pos;
			pos += len;
			if (n - pos < 4)
				die("dump-flush: truncated token count");
			ntok = le32(in + pos);
			pos += 4;
			if (ntok > len || (n - pos) / 4 < ntok)
				die("dump-flush: bad token count");
			deflate_begin_sequences(c, seq);
			for (u32 t = 0; t < ntok; t++, pos += 4) {
				u32 tl = le16(in + pos), to = le16(in + pos + 2);

				if (tl == 0) {
					if (to != 0 || used >= len)
						die("dump-flush: bad literal token");
					deflate_choose_literal(c, data[used], false, seq);
					used++;
				} else {
					if (tl < DEFLATE_MIN_MATCH_LEN ||
					    tl > DEFLATE_MAX_MATCH_LEN || to < 1 ||
					    to > DEFLATE_MAX_MATCH_OFFSET ||
					    len - used < tl)
						die("dump-flush: bad match token");
					deflate_choose_match(c, tl, to, false, &seq);
					used += tl;
				}
			}
			if (used != len)
				die("dump-flush: tokens do not cover the block");
			if (!os.overflow)
				deflate_finish_block(c, &os, data, len, seqs, final);
		}
		write_all(buf, cap);
		printf("next=%zu bitcount=%u bitbuf=%016llx overflow=%d\n",
		       (size_t)(os.next - buf), os.bitcount,
		       (unsigned long long)os.bitbuf, os.overflow ? 1 : 0);
	}
	return 0;
}

/*
 * compress-bound -l N: libdeflate_deflate_compress_bound on a level N
 * compressor. Input: u64 LE sizes until EOF; output: one u64 LE bound each.
 */
static int cmd_compress_bound(int argc, char **argv)
{
	struct opts o;
	unsigned char *in;
	size_t n;
	struct libdeflate_compressor *c;

	parse_opts(argc, argv, &o);
	if (o.level < 0 || o.level > 12)
		die("compress-bound: -l N (0..12) required");
	in = read_all(stdin, &n);
	if (n % 8)
		die("compress-bound: input is not a list of u64");
	c = level_compressor(o.level);
	for (size_t i = 0; i < n; i += 8) {
		uint64_t b = libdeflate_deflate_compress_bound(c, (size_t)le64(in + i));
		unsigned char o8[8];

		for (int k = 0; k < 8; k++)
			o8[k] = (unsigned char)(b >> (8 * k));
		write_all(o8, 8);
	}
	return 0;
}

/*
 * dump-params -l N: what libdeflate_alloc_compressor(N) sets, as one line
 *   "level=<compression_level> passthrough=<max_passthrough_size, %zu>
 *    impl=<none|fastest|greedy|lazy|lazy2|near_optimal>"
 * followed, for levels >= 2, by " depth=<max_search_depth>", for levels >= 1
 * by " nice=<nice_match_length>", and for levels >= 10 by
 *   " passes=<p.n.max_optim_passes> minimp=<p.n.min_improvement_to_continue>
 *    minbits=<p.n.min_bits_to_use_nonfinal_path>
 *    maxstatic=<p.n.max_len_to_optimize_static_block>
 *    slotfull=<sum of p.n.offset_slot_full[off] * off, off = 1..32768>"
 * (all on one line). Fields a level leaves unset are not printed: they are
 * uninitialized malloc memory, and p.n is not even allocated below level 10.
 */
static int cmd_dump_params(int argc, char **argv)
{
	struct opts o;
	struct libdeflate_compressor *c;
	const char *impl = "none";

	parse_opts(argc, argv, &o);
	if (o.level < 0 || o.level > 12)
		die("dump-params: -l N (0..12) required");
	c = level_compressor(o.level);
	if (c->impl == deflate_compress_fastest)
		impl = "fastest";
	else if (c->impl == deflate_compress_greedy)
		impl = "greedy";
	else if (c->impl == deflate_compress_lazy)
		impl = "lazy";
	else if (c->impl == deflate_compress_lazy2)
		impl = "lazy2";
	else if (c->impl == deflate_compress_near_optimal)
		impl = "near_optimal";
	else if (c->impl != NULL)
		die("dump-params: unknown impl");
	printf("level=%u passthrough=%zu impl=%s", c->compression_level,
	       c->max_passthrough_size, impl);
	if (o.level >= 2)
		printf(" depth=%u", (unsigned)c->max_search_depth);
	if (o.level >= 1)
		printf(" nice=%u", (unsigned)c->nice_match_length);
	if (o.level >= 10) {
		unsigned long long sum = 0;

		for (u32 off = 1; off <= DEFLATE_MAX_MATCH_OFFSET; off++)
			sum += (unsigned long long)c->p.n.offset_slot_full[off] * off;
		printf(" passes=%u minimp=%u minbits=%u maxstatic=%u slotfull=%llu",
		       c->p.n.max_optim_passes,
		       (unsigned)c->p.n.min_improvement_to_continue,
		       (unsigned)c->p.n.min_bits_to_use_nonfinal_path,
		       (unsigned)c->p.n.max_len_to_optimize_static_block, sum);
	}
	printf("\n");
	return 0;
}

/*
 * compress-batch: many raw compressions in one process (T2.3 differential
 * tests). Input (stdin), records until EOF, each:
 *   u8 level (0..12), u32 LE cap (output buffer size; 0xFFFFFFFF means
 *   libdeflate_deflate_compress_bound(c, n)), u32 LE n, n data bytes.
 * Output per record: u32 LE r (the libdeflate_deflate_compress return value,
 * 0 if the output did not fit), then the r output bytes. One compressor per
 * level is reused across records (as the Go tests reuse theirs). Malformed
 * input exits 1.
 */
static int cmd_compress_batch(int argc, char **argv)
{
	unsigned char *in, *out;
	size_t n, pos, maxcap = 1;

	(void)argc, (void)argv;
	in = read_all(stdin, &n);
	/* Pass 1: validate and size the one output buffer. */
	for (pos = 0; pos < n;) {
		size_t len, cap;

		if (n - pos < 9 || in[pos] > 12)
			die("compress-batch: bad record header");
		cap = le32(in + pos + 1);
		len = le32(in + pos + 5);
		if (n - pos - 9 < len)
			die("compress-batch: truncated record");
		if (cap == 0xFFFFFFFFu)
			cap = libdeflate_deflate_compress_bound(NULL, len);
		if (cap > maxcap)
			maxcap = cap;
		pos += 9 + len;
	}
	out = xmalloc(maxcap);
	for (pos = 0; pos < n;) {
		struct libdeflate_compressor *c = level_compressor(in[pos]);
		size_t cap = le32(in + pos + 1), len = le32(in + pos + 5), r;
		unsigned char r4[4];

		if (cap == 0xFFFFFFFFu)
			cap = libdeflate_deflate_compress_bound(c, len);
		r = libdeflate_deflate_compress(c, in + pos + 9, len, out, cap);
		for (int k = 0; k < 4; k++)
			r4[k] = (unsigned char)(r >> (8 * k));
		write_all(r4, 4);
		write_all(out, r);
		pos += 9 + len;
	}
	return 0;
}

/*
 * dump-htmf: compresses stdin (more than 51 bytes, so that the level 1
 * compressor really runs; at most 51 bytes exits 1) at level 1 into a
 * compress_bound buffer, then writes the final state of the ht_matchfinder,
 * c->p.f.ht_mf.hash_tab, as (1 << HT_MATCHFINDER_HASH_ORDER) *
 * HT_MATCHFINDER_BUCKET_SIZE s16 LE values in C's row-major order
 * (hash_tab[h][0], hash_tab[h][1], ...). No compressed data is written.
 */
static int cmd_dump_htmf(int argc, char **argv)
{
	unsigned char *in, *out;
	size_t n, bound;
	struct libdeflate_compressor *c;
	const mf_pos_t *tab;
	size_t nent = sizeof(c->p.f.ht_mf.hash_tab) / sizeof(mf_pos_t);

	(void)argc, (void)argv;
	in = read_all(stdin, &n);
	c = level_compressor(1);
	if (n <= c->max_passthrough_size)
		die("dump-htmf: input too short for the level 1 compressor");
	bound = libdeflate_deflate_compress_bound(c, n);
	out = xmalloc(bound);
	if (libdeflate_deflate_compress(c, in, n, out, bound) == 0)
		die("dump-htmf: compress failed");
	tab = &c->p.f.ht_mf.hash_tab[0][0];
	for (size_t i = 0; i < nent; i++) {
		unsigned char b2[2] = { (unsigned char)((u16)tab[i]),
					(unsigned char)((u16)tab[i] >> 8) };

		write_all(b2, 2);
	}
	return 0;
}

/* ---- greedy compressor: block splitting, min_len, hc_matchfinder (T2.4) --- */

#define SPLIT_MAX_POS	(1u << 24)
#define SPLIT_MAX_COUNT	(1u << 22)

/*
 * dump-split: runs a script of operations on ONE struct block_split_stats
 * through the real static functions of deflate_compress.c. Input (stdin), ops
 * until EOF; positions are byte indices (<= 2^24) into one buffer, passed to
 * the C functions as pointers (in_block_begin, in_next, in_end). Ops:
 *   'i'                     init_block_split_stats
 *   'l' u8 lit              observe_literal(lit)
 *   'L' u8 lit, u32 count   observe_literal(lit), count times
 *   'm' u32 len             observe_match(len)
 *   'M' u32 len, u32 count  observe_match(len), count times
 *   'g'                     merge_new_observations
 *   'r' u32 begin, u32 next, u32 end   ready_to_check_block -> u8 0/1
 *   's' u32 begin, u32 next, u32 end   should_end_block -> u8 0/1
 *   'd' u32 block_length    do_end_block_check -> u8 0/1
 *   'p'                     -> the stats as 22 u32 LE: new_observations[10],
 *                              observations[10], num_new_observations,
 *                              num_observations
 * (all u32 LE). Only the 'r'/'s'/'d'/'p' results are written. The stats start
 * zeroed. Malformed input, including an 'L'/'M' count above SPLIT_MAX_COUNT
 * (2^22), exits 1.
 */
static int cmd_dump_split(int argc, char **argv)
{
	unsigned char *in, *base;
	size_t n, pos = 0;
	struct block_split_stats st;

	(void)argv;
	if (argc != 2)
		die("dump-split takes no options");
	in = read_all(stdin, &n);
	base = xmalloc(SPLIT_MAX_POS + 1);
	memset(&st, 0, sizeof(st));
	while (pos < n) {
		unsigned char op = in[pos++], r;
		u32 a, b, e;

		switch (op) {
		case 'i':
			init_block_split_stats(&st);
			break;
		case 'l':
			if (n - pos < 1)
				die("dump-split: truncated op");
			observe_literal(&st, in[pos]);
			pos += 1;
			break;
		case 'L':
			if (n - pos < 5)
				die("dump-split: truncated op");
			if (le32(in + pos + 1) > SPLIT_MAX_COUNT)
				die("dump-split: repeat count too large");
			for (a = le32(in + pos + 1); a > 0; a--)
				observe_literal(&st, in[pos]);
			pos += 5;
			break;
		case 'm':
			if (n - pos < 4)
				die("dump-split: truncated op");
			observe_match(&st, le32(in + pos));
			pos += 4;
			break;
		case 'M':
			if (n - pos < 8)
				die("dump-split: truncated op");
			if (le32(in + pos + 4) > SPLIT_MAX_COUNT)
				die("dump-split: repeat count too large");
			for (a = le32(in + pos + 4); a > 0; a--)
				observe_match(&st, le32(in + pos));
			pos += 8;
			break;
		case 'g':
			merge_new_observations(&st);
			break;
		case 'r':
		case 's':
			if (n - pos < 12)
				die("dump-split: truncated op");
			a = le32(in + pos);
			b = le32(in + pos + 4);
			e = le32(in + pos + 8);
			pos += 12;
			if (a > b || b > e || e > SPLIT_MAX_POS)
				die("dump-split: bad positions");
			if (op == 'r')
				r = ready_to_check_block(&st, base + a, base + b, base + e);
			else
				r = should_end_block(&st, base + a, base + b, base + e);
			write_all(&r, 1);
			break;
		case 'd':
			if (n - pos < 4)
				die("dump-split: truncated op");
			r = do_end_block_check(&st, le32(in + pos));
			pos += 4;
			write_all(&r, 1);
			break;
		case 'p':
			put_u32s(st.new_observations, NUM_OBSERVATION_TYPES);
			put_u32s(st.observations, NUM_OBSERVATION_TYPES);
			put_u32s(&st.num_new_observations, 1);
			put_u32s(&st.num_observations, 1);
			break;
		default:
			die("dump-split: unknown op");
		}
	}
	return 0;
}

/*
 * dump-minlen: the min-match-length heuristic. Input (stdin), records until
 * EOF, each one of:
 *   'c' u32 num_used_literals, u32 max_search_depth
 *         -> choose_min_match_len
 *   'a' u32 max_search_depth, u32 n, n data bytes
 *         -> calculate_min_match_len(data, n, max_search_depth)
 *   'r' u32 max_search_depth, 256 x u32 litlen frequencies
 *         -> recalculate_min_match_len (a struct deflate_freqs with these
 *            literal frequencies, all others 0)
 * (all u32 LE). Output: one u32 LE result per record. Malformed input exits 1.
 */
static int cmd_dump_minlen(int argc, char **argv)
{
	unsigned char *in;
	size_t n, pos = 0;

	(void)argv;
	if (argc != 2)
		die("dump-minlen takes no options");
	in = read_all(stdin, &n);
	while (pos < n) {
		unsigned char op = in[pos++];
		u32 r, len;
		struct deflate_freqs f;

		switch (op) {
		case 'c':
			if (n - pos < 8)
				die("dump-minlen: truncated record");
			r = choose_min_match_len(le32(in + pos), le32(in + pos + 4));
			pos += 8;
			break;
		case 'a':
			if (n - pos < 8)
				die("dump-minlen: truncated record");
			len = le32(in + pos + 4);
			if (n - pos - 8 < len)
				die("dump-minlen: truncated data");
			r = calculate_min_match_len(in + pos + 8, len, le32(in + pos));
			pos += 8 + (size_t)len;
			break;
		case 'r':
			if (n - pos < 4 + 4 * DEFLATE_NUM_LITERALS)
				die("dump-minlen: truncated record");
			memset(&f, 0, sizeof(f));
			for (int i = 0; i < DEFLATE_NUM_LITERALS; i++)
				f.litlen[i] = le32(in + pos + 4 + 4 * i);
			r = recalculate_min_match_len(&f, le32(in + pos));
			pos += 4 + 4 * DEFLATE_NUM_LITERALS;
			break;
		default:
			die("dump-minlen: unknown record");
		}
		put_u32s(&r, 1);
	}
	return 0;
}

/*
 * dump-hcmf: drives the real hc_matchfinder (hc_matchfinder_init,
 * hc_matchfinder_longest_match, hc_matchfinder_skip_bytes; slides happen
 * inside them) and dumps its state. Input (stdin), ops until EOF, all
 * integers u32 LE:
 *   'b' n, n bytes   new buffer (these n input bytes), and resets a standalone
 *                    32-byte aligned struct hc_matchfinder: next_tab zeroed
 *                    (hc_matchfinder_init does not initialize it, so entries
 *                    never written would be uninitialized memory), then
 *                    hc_matchfinder_init; in_base = buffer start,
 *                    next_hashes = {0, 0}.
 *   'f' pos, best_len, max_len, nice_len, depth
 *                    hc_matchfinder_longest_match(mf, &in_base, buf + pos,
 *                    best_len, max_len, nice_len, depth, next_hashes, &off)
 *                    -> u16 LE length, u16 LE offset.
 *   'k' pos, count   hc_matchfinder_skip_bytes(mf, &in_base, buf + pos,
 *                    buf + n, count, next_hashes); no output.
 *   'h'              -> u64 LE FNV-1a (offset basis 0xcbf29ce484222325, prime
 *                    0x100000001b3) over the bytes of hash3_tab, hash4_tab and
 *                    next_tab as s16 LE, then u32 in_base - buf,
 *                    next_hashes[0], next_hashes[1].
 *   'c' level (u8, 2..9), full (u8, 0/1)
 *                    compresses the buffer (more than max_passthrough_size
 *                    bytes, else exits 1) with libdeflate_deflate_compress on a
 *                    FRESH level compressor whose c->p.g.hc_mf.next_tab is
 *                    zeroed first, into a compress_bound buffer; then writes
 *                    c->p.g.hc_mf: full = 1: every entry of hash3_tab
 *                    (1 << 15), hash4_tab (1 << 16), next_tab (32768) as s16 LE;
 *                    full = 0: the u64 LE FNV-1a of those bytes.
 * The caller must keep the matchfinder's contract (as the compressors do):
 * positions are visited in order, each by one 'f' or as part of one 'k'
 * (count >= 1), max_len <= n - pos, nice_len <= max_len, depth >= 1. The
 * order is enforced (each 'f' at exactly the next position, each 'k' starting
 * there and ending within the buffer; 'b' restarts at 0), and the window
 * checks reject what would make C index out of its tables: an 'f' with
 * max_len >= 5, or a 'k', more than a window past in_base. Malformed input
 * and any violation exit 1.
 */
static uint64_t hcmf_hash(const struct hc_matchfinder *mf)
{
	const mf_pos_t *tab = (const mf_pos_t *)mf;
	uint64_t h = 0xcbf29ce484222325ull;

	for (size_t i = 0; i < sizeof(*mf) / sizeof(mf_pos_t); i++) {
		h = (h ^ (u8)((u16)tab[i])) * 0x100000001b3ull;
		h = (h ^ (u8)((u16)tab[i] >> 8)) * 0x100000001b3ull;
	}
	return h;
}

static void put_u64(uint64_t v)
{
	unsigned char b[8];

	for (int k = 0; k < 8; k++)
		b[k] = (unsigned char)(v >> (8 * k));
	write_all(b, 8);
}

static int cmd_dump_hcmf(int argc, char **argv)
{
	unsigned char *in, *buf = NULL, *out = NULL;
	size_t n, pos = 0, blen = 0, next_pos = 0;
	struct hc_matchfinder *mf = NULL;
	const u8 *in_base = NULL;
	u32 next_hashes[2] = { 0, 0 };
	void *p;

	(void)argv;
	if (argc != 2)
		die("dump-hcmf takes no options");
	in = read_all(stdin, &n);
	if (posix_memalign(&p, MATCHFINDER_MEM_ALIGNMENT, sizeof(*mf)) != 0)
		die("out of memory");
	mf = track(p);
	while (pos < n) {
		unsigned char op = in[pos++];
		u32 a, best, maxl, nice, depth, len, off = 0;
		unsigned char r4[4];

		switch (op) {
		case 'b':
			if (n - pos < 4 || n - pos - 4 < le32(in + pos))
				die("dump-hcmf: truncated buffer");
			blen = le32(in + pos);
			buf = in + pos + 4; /* the input stays allocated */
			pos += 4 + blen;
			memset(mf->next_tab, 0, sizeof(mf->next_tab));
			hc_matchfinder_init(mf);
			in_base = buf;
			next_hashes[0] = next_hashes[1] = 0;
			next_pos = 0;
			break;
		case 'f':
			if (n - pos < 20 || !buf)
				die("dump-hcmf: truncated op");
			a = le32(in + pos);
			best = le32(in + pos + 4);
			maxl = le32(in + pos + 8);
			nice = le32(in + pos + 12);
			depth = le32(in + pos + 16);
			pos += 20;
			if (a != next_pos || a >= blen || maxl > blen - a || nice > maxl || depth < 1 ||
			    (maxl >= 5 && (size_t)(buf + a - in_base) > MATCHFINDER_WINDOW_SIZE))
				die("dump-hcmf: bad longest_match op");
			next_pos = (size_t)a + 1;
			len = hc_matchfinder_longest_match(mf, &in_base, buf + a, best,
							   maxl, nice, depth,
							   next_hashes, &off);
			r4[0] = (unsigned char)len;
			r4[1] = (unsigned char)(len >> 8);
			r4[2] = (unsigned char)off;
			r4[3] = (unsigned char)(off >> 8);
			write_all(r4, 4);
			break;
		case 'k':
			if (n - pos < 8 || !buf)
				die("dump-hcmf: truncated op");
			a = le32(in + pos);
			len = le32(in + pos + 4);
			pos += 8;
			if (a != next_pos || a >= blen || len < 1 || len > blen - a ||
			    (size_t)(buf + a - in_base) > MATCHFINDER_WINDOW_SIZE)
				die("dump-hcmf: bad skip_bytes op");
			next_pos = (size_t)a + len;
			hc_matchfinder_skip_bytes(mf, &in_base, buf + a, buf + blen,
						  len, next_hashes);
			break;
		case 'h':
			put_u64(hcmf_hash(mf));
			a = buf ? (u32)(in_base - buf) : 0;
			put_u32s(&a, 1);
			put_u32s(next_hashes, 2);
			break;
		case 'c': {
			struct libdeflate_compressor *c;
			unsigned level, full;
			size_t bound;

			if (n - pos < 2 || !buf)
				die("dump-hcmf: truncated op");
			level = in[pos];
			full = in[pos + 1];
			pos += 2;
			if (level < 2 || level > 9 || full > 1)
				die("dump-hcmf: bad compress op");
			if (g_c)
				libdeflate_free_compressor(g_c);
			g_c = NULL;
			c = level_compressor((int)level);
			if (blen <= c->max_passthrough_size)
				die("dump-hcmf: input too short for the compressor to run");
			memset(c->p.g.hc_mf.next_tab, 0, sizeof(c->p.g.hc_mf.next_tab));
			bound = libdeflate_deflate_compress_bound(c, blen);
			if (!out) /* n >= blen: large enough for every 'c' */
				out = xmalloc(libdeflate_deflate_compress_bound(NULL, n));
			if (libdeflate_deflate_compress(c, buf, blen, out, bound) == 0)
				die("dump-hcmf: compress failed");
			if (full) {
				const mf_pos_t *tab = (const mf_pos_t *)&c->p.g.hc_mf;

				for (size_t i = 0; i < sizeof(c->p.g.hc_mf) / sizeof(mf_pos_t); i++) {
					unsigned char b2[2] = { (unsigned char)((u16)tab[i]),
								(unsigned char)((u16)tab[i] >> 8) };

					write_all(b2, 2);
				}
			} else {
				put_u64(hcmf_hash(&c->p.g.hc_mf));
			}
			break;
		}
		default:
			die("dump-hcmf: unknown op");
		}
	}
	return 0;
}

/* ---- near-optimal compressor: bt_matchfinder, costs, optimizer (T2.6) ---- */

/*
 * dump-btmf: drives the real bt_matchfinder (bt_matchfinder_init,
 * bt_matchfinder_get_matches, bt_matchfinder_skip_byte,
 * bt_matchfinder_slide_window) and dumps its state. Input (stdin), ops until
 * EOF, all integers u32 LE:
 *   'b' n, n bytes   new buffer (these n input bytes), and resets a standalone
 *                    32-byte aligned struct bt_matchfinder: child_tab zeroed
 *                    (bt_matchfinder_init does not initialize it), then
 *                    bt_matchfinder_init; in_base = buffer start,
 *                    next_hashes = {0, 0}.
 *   'g' pos, max_len, nice_len, depth
 *                    bt_matchfinder_get_matches(mf, in_base, buf + pos -
 *                    in_base, max_len, nice_len, depth, next_hashes, m)
 *                    -> u16 LE count, then count x (u16 LE length, u16 LE
 *                    offset).
 *   'k' pos, nice_len, depth
 *                    bt_matchfinder_skip_byte(mf, in_base, buf + pos -
 *                    in_base, nice_len, depth, next_hashes); no output.
 *   'h'              -> u64 LE FNV-1a (as dump-hcmf) over the bytes of
 *                    hash3_tab, hash4_tab and child_tab as s16 LE, then u32
 *                    in_base - buf, next_hashes[0], next_hashes[1].
 *   'c' level (u8, 10..12), full (u8, 0/1)
 *                    compresses the buffer (more than max_passthrough_size
 *                    bytes, else exits 1) with libdeflate_deflate_compress on a
 *                    FRESH level compressor whose c->p.n.bt_mf.child_tab is
 *                    zeroed first, into a compress_bound buffer; then writes
 *                    c->p.n.bt_mf: full = 1: every entry of hash3_tab
 *                    (2 << 16, row-major), hash4_tab (1 << 16) and child_tab
 *                    (65536) as s16 LE; full = 0: the u64 LE FNV-1a of those
 *                    bytes.
 * Before each 'g' / 'k' the window is slid exactly as
 * deflate_compress_near_optimal does: when pos - in_base == 32768,
 * bt_matchfinder_slide_window and in_base = buf + pos. The caller must keep
 * the matchfinder's contract (as the compressor does), which is enforced:
 * every position is visited in order by exactly one 'g' or 'k' ('b' restarts
 * at 0); 'g': 5 <= max_len <= n - pos, 1 <= nice_len <= max_len, depth >= 1;
 * 'k': 5 <= nice_len <= n - pos, depth >= 1. Malformed input and any
 * violation exit 1.
 */
static uint64_t btmf_hash(const struct bt_matchfinder *mf)
{
	const mf_pos_t *tab = (const mf_pos_t *)mf;
	uint64_t h = 0xcbf29ce484222325ull;

	for (size_t i = 0; i < sizeof(*mf) / sizeof(mf_pos_t); i++) {
		h = (h ^ (u8)((u16)tab[i])) * 0x100000001b3ull;
		h = (h ^ (u8)((u16)tab[i] >> 8)) * 0x100000001b3ull;
	}
	return h;
}

static int cmd_dump_btmf(int argc, char **argv)
{
	unsigned char *in, *buf = NULL, *out = NULL;
	size_t n, pos = 0, blen = 0, next_pos = 0;
	struct bt_matchfinder *mf = NULL;
	const u8 *in_base = NULL;
	u32 next_hashes[2] = { 0, 0 };
	struct lz_match m[MAX_MATCHES_PER_POS];
	void *p;

	(void)argv;
	if (argc != 2)
		die("dump-btmf takes no options");
	in = read_all(stdin, &n);
	if (posix_memalign(&p, MATCHFINDER_MEM_ALIGNMENT, sizeof(*mf)) != 0)
		die("out of memory");
	mf = track(p);
	while (pos < n) {
		unsigned char op = in[pos++];
		u32 a, maxl, nice, depth;

		switch (op) {
		case 'b':
			if (n - pos < 4 || n - pos - 4 < le32(in + pos))
				die("dump-btmf: truncated buffer");
			blen = le32(in + pos);
			buf = in + pos + 4; /* the input stays allocated */
			pos += 4 + blen;
			memset(mf->child_tab, 0, sizeof(mf->child_tab));
			bt_matchfinder_init(mf);
			in_base = buf;
			next_hashes[0] = next_hashes[1] = 0;
			next_pos = 0;
			break;
		case 'g':
		case 'k': {
			if (!buf || n - pos < (op == 'g' ? 16u : 12u))
				die("dump-btmf: truncated op");
			a = le32(in + pos);
			if (op == 'g') {
				maxl = le32(in + pos + 4);
				nice = le32(in + pos + 8);
				depth = le32(in + pos + 12);
				pos += 16;
				if (a != next_pos || a >= blen || maxl < BT_MATCHFINDER_REQUIRED_NBYTES ||
				    maxl > blen - a || nice < 1 || nice > maxl || depth < 1)
					die("dump-btmf: bad get_matches op");
			} else {
				nice = le32(in + pos + 4);
				depth = le32(in + pos + 8);
				pos += 12;
				if (a != next_pos || a >= blen || nice < BT_MATCHFINDER_REQUIRED_NBYTES ||
				    nice > blen - a || depth < 1)
					die("dump-btmf: bad skip_byte op");
			}
			next_pos = (size_t)a + 1;
			if (buf + a - in_base == MATCHFINDER_WINDOW_SIZE) {
				bt_matchfinder_slide_window(mf);
				in_base = buf + a;
			}
			if (op == 'g') {
				struct lz_match *end = bt_matchfinder_get_matches(
					mf, in_base, buf + a - in_base, maxl, nice, depth,
					next_hashes, m);
				u16 cnt = (u16)(end - m);
				unsigned char b2[2] = { (unsigned char)cnt, (unsigned char)(cnt >> 8) };

				write_all(b2, 2);
				for (u16 i = 0; i < cnt; i++) {
					unsigned char r4[4] = {
						(unsigned char)m[i].length, (unsigned char)(m[i].length >> 8),
						(unsigned char)m[i].offset, (unsigned char)(m[i].offset >> 8) };

					write_all(r4, 4);
				}
			} else {
				bt_matchfinder_skip_byte(mf, in_base, buf + a - in_base, nice,
							 depth, next_hashes);
			}
			break;
		}
		case 'h':
			put_u64(btmf_hash(mf));
			a = buf ? (u32)(in_base - buf) : 0;
			put_u32s(&a, 1);
			put_u32s(next_hashes, 2);
			break;
		case 'c': {
			struct libdeflate_compressor *c;
			unsigned level, full;
			size_t bound;

			if (n - pos < 2 || !buf)
				die("dump-btmf: truncated op");
			level = in[pos];
			full = in[pos + 1];
			pos += 2;
			if (level < 10 || level > 12 || full > 1)
				die("dump-btmf: bad compress op");
			if (g_c)
				libdeflate_free_compressor(g_c);
			g_c = NULL;
			c = level_compressor((int)level);
			if (blen <= c->max_passthrough_size)
				die("dump-btmf: input too short for the compressor to run");
			memset(c->p.n.bt_mf.child_tab, 0, sizeof(c->p.n.bt_mf.child_tab));
			bound = libdeflate_deflate_compress_bound(c, blen);
			if (!out) /* n >= blen: large enough for every 'c' */
				out = xmalloc(libdeflate_deflate_compress_bound(NULL, n));
			if (libdeflate_deflate_compress(c, buf, blen, out, bound) == 0)
				die("dump-btmf: compress failed");
			if (full) {
				const mf_pos_t *tab = (const mf_pos_t *)&c->p.n.bt_mf;

				for (size_t i = 0; i < sizeof(c->p.n.bt_mf) / sizeof(mf_pos_t); i++) {
					unsigned char b2[2] = { (unsigned char)((u16)tab[i]),
								(unsigned char)((u16)tab[i] >> 8) };

					write_all(b2, 2);
				}
			} else {
				put_u64(btmf_hash(&c->p.n.bt_mf));
			}
			break;
		}
		default:
			die("dump-btmf: unknown op");
		}
	}
	return 0;
}

/*
 * dump-nopt: the near-optimal parser's static functions on the state of one
 * real compressor (c->p.n, c->split_stats, c->freqs, c->codes), driven by a
 * script. Input (stdin), ops until EOF, all integers u32 LE unless noted:
 *   'L' level (u8, 10..12)  selects the compressor (libdeflate_alloc_compressor
 *                    (level); kept, with all its state, while the level stays
 *                    the same). Every other op needs one.
 *   'C' 256 + 259 + 32 costs   c->p.n.costs.literal, .length, .offset_slot := .
 *   'P'              -> the 256 + 259 + 32 u32 of c->p.n.costs.
 *   'f' 320 x u8 lens          deflate_set_costs_from_codes(c, lens) (the litlen
 *                    then the offset codeword lengths, struct deflate_lens).
 *   'd' lit_cost, len_sym_cost deflate_set_default_costs.
 *   'a' lit_cost, len_sym_cost deflate_adjust_costs.
 *   'S' 10 observations, num_observations, 10 new_observations,
 *       num_new_observations, 10 prev_observations, prev_num_observations,
 *       259 match_len_freqs, 259 new_match_len_freqs
 *                    sets c->split_stats and those c->p.n fields.
 *   'T'              -> the same 551 u32 (current values).
 *   'i' / 'm' / 's' / 'x'      deflate_near_optimal_init_stats / _merge_stats /
 *                    _save_stats / _clear_old_stats.
 *   'l' n, n bytes   deflate_choose_default_litlen_costs(c, data, n, &lit, &len)
 *                    -> u32 lit_cost, u32 len_sym_cost.
 *   'I' first (u8), n, n bytes  deflate_set_initial_costs(c, data, n, first).
 *   't' 288 + 32 freqs         c->freqs := freqs; deflate_make_huffman_codes(
 *                    &c->freqs, &c->codes); deflate_compute_true_cost(c)
 *                    -> u32 cost, then the 320 bytes of c->codes.lens.
 *   'p' bl, ncache, ncache x (u16 length, u16 offset)
 *                    a match cache of exactly bl positions (see below) copied to
 *                    c->p.n.match_cache; optimum_nodes[bl ..
 *                    min(bl - 1 + 258, MAX_BLOCK_LENGTH)].cost_to_end :=
 *                    0x80000000 (as deflate_optimize_and_flush_block does);
 *                    deflate_find_min_cost_path(c, bl, cache + ncache)
 *                    -> bl x (u32 cost_to_end, u32 item) of nodes 0..bl-1 (the
 *                    end node's item is never written), the 288 + 32 u32 of
 *                    c->freqs, the 320 bytes of c->codes.lens.
 *   'o' first (u8), final (u8), cap (<= 8 MiB), bl, bl bytes, ncache, ncache x
 *       (u16 length, u16 offset)
 *                    the cache as for 'p'; deflate_optimize_and_flush_block(c,
 *                    &os, data, bl, cache + ncache, first, final, &used) into a
 *                    cap-byte buffer (filled 0xA5, out_next at 0, bitcount 0)
 *                    -> u8 used, u32 next, u8 bitcount, u64 bitbuf, u8
 *                    overflow, the next output bytes, then the 547 u32 costs.
 * A match cache of bl positions (1 <= bl <= MAX_BLOCK_LENGTH) is, for each
 * position in order, its k <= MAX_MATCHES_PER_POS matches (lengths strictly
 * increasing in 3..258, offsets 1..32768, position + length <=
 * MAX_BLOCK_LENGTH) then a header (length k, offset = the literal, <= 255),
 * as deflate_compress_near_optimal builds it; it must end exactly at ncache
 * entries. Malformed input exits 1.
 */
#define NOPT_NCOSTS (DEFLATE_NUM_LITERALS + DEFLATE_MAX_MATCH_LEN + 1 + DEFLATE_NUM_OFFSET_SYMS)
#define NOPT_NSTATS (3 * (NUM_OBSERVATION_TYPES + 1) + 2 * (DEFLATE_MAX_MATCH_LEN + 1))

static void nopt_put_costs(const struct deflate_costs *k)
{
	put_u32s(k->literal, DEFLATE_NUM_LITERALS);
	put_u32s(k->length, DEFLATE_MAX_MATCH_LEN + 1);
	put_u32s(k->offset_slot, DEFLATE_NUM_OFFSET_SYMS);
}

/* Reads and validates a match cache of bl positions at in[*pos..]; returns
 * the number of entries, copied into c->p.n.match_cache. */
static u32 nopt_read_cache(struct libdeflate_compressor *c, const unsigned char *in,
			   size_t n, size_t *pos, u32 bl)
{
	u32 ncache, idx, p;
	struct lz_match *mc = c->p.n.match_cache;

	if (bl < 1 || bl > MAX_BLOCK_LENGTH || n - *pos < 4)
		die("dump-nopt: bad block length");
	ncache = le32(in + *pos);
	*pos += 4;
	if (ncache > ARRAY_LEN(c->p.n.match_cache) || (n - *pos) / 4 < ncache)
		die("dump-nopt: bad cache length");
	for (u32 i = 0; i < ncache; i++) {
		mc[i].length = le16(in + *pos + 4 * (size_t)i);
		mc[i].offset = le16(in + *pos + 4 * (size_t)i + 2);
	}
	*pos += 4 * (size_t)ncache;
	/* Walk the positions backwards, as deflate_find_min_cost_path does. */
	idx = ncache;
	for (p = bl; p-- > 0;) {
		u32 k, prev = 0;

		if (idx < 1)
			die("dump-nopt: cache too short");
		k = mc[--idx].length;
		if (k > MAX_MATCHES_PER_POS || mc[idx].offset > 255 || k > idx)
			die("dump-nopt: bad cache header");
		for (u32 j = idx - k; j < idx; j++) {
			if (mc[j].length < DEFLATE_MIN_MATCH_LEN ||
			    mc[j].length > DEFLATE_MAX_MATCH_LEN || mc[j].length <= prev ||
			    mc[j].offset < 1 || mc[j].offset > DEFLATE_MAX_MATCH_OFFSET ||
			    p + mc[j].length > MAX_BLOCK_LENGTH)
				die("dump-nopt: bad cached match");
			prev = mc[j].length;
		}
		idx -= k;
	}
	if (idx != 0)
		die("dump-nopt: cache longer than the block");
	return ncache;
}

static void nopt_init_beyond(struct libdeflate_compressor *c, u32 bl)
{
	for (u32 i = bl; i <= MIN(bl - 1 + DEFLATE_MAX_MATCH_LEN,
				  ARRAY_LEN(c->p.n.optimum_nodes) - 1); i++)
		c->p.n.optimum_nodes[i].cost_to_end = 0x80000000;
}

static int cmd_dump_nopt(int argc, char **argv)
{
	unsigned char *in, *buf = NULL;
	size_t n, pos = 0;
	struct libdeflate_compressor *c = NULL;

	(void)argv;
	if (argc != 2)
		die("dump-nopt takes no options");
	in = read_all(stdin, &n);
	while (pos < n) {
		unsigned char op = in[pos++];
		u32 a, b, bl, ncache;

		if (op != 'L' && !c)
			die("dump-nopt: no compressor selected");
		switch (op) {
		case 'L':
			if (n - pos < 1 || in[pos] < 10 || in[pos] > 12)
				die("dump-nopt: bad level");
			c = level_compressor(in[pos]);
			pos++;
			break;
		case 'C':
			if ((n - pos) / 4 < NOPT_NCOSTS)
				die("dump-nopt: truncated costs");
			for (u32 i = 0; i < DEFLATE_NUM_LITERALS; i++, pos += 4)
				c->p.n.costs.literal[i] = le32(in + pos);
			for (u32 i = 0; i <= DEFLATE_MAX_MATCH_LEN; i++, pos += 4)
				c->p.n.costs.length[i] = le32(in + pos);
			for (u32 i = 0; i < DEFLATE_NUM_OFFSET_SYMS; i++, pos += 4)
				c->p.n.costs.offset_slot[i] = le32(in + pos);
			break;
		case 'P':
			nopt_put_costs(&c->p.n.costs);
			break;
		case 'f': {
			struct deflate_lens lens;

			if (n - pos < DEFLATE_NUM_LITLEN_SYMS + DEFLATE_NUM_OFFSET_SYMS)
				die("dump-nopt: truncated lens");
			memcpy(lens.litlen, in + pos, DEFLATE_NUM_LITLEN_SYMS);
			memcpy(lens.offset, in + pos + DEFLATE_NUM_LITLEN_SYMS, DEFLATE_NUM_OFFSET_SYMS);
			pos += DEFLATE_NUM_LITLEN_SYMS + DEFLATE_NUM_OFFSET_SYMS;
			deflate_set_costs_from_codes(c, &lens);
			break;
		}
		case 'd':
		case 'a':
			if (n - pos < 8)
				die("dump-nopt: truncated op");
			a = le32(in + pos);
			b = le32(in + pos + 4);
			pos += 8;
			if (op == 'd')
				deflate_set_default_costs(c, a, b);
			else
				deflate_adjust_costs(c, a, b);
			break;
		case 'S':
		case 'T': {
			u32 *fields[6] = { c->split_stats.observations, &c->split_stats.num_observations,
					   c->split_stats.new_observations,
					   &c->split_stats.num_new_observations,
					   c->p.n.prev_observations, &c->p.n.prev_num_observations };
			u32 *freqs[2] = { c->p.n.match_len_freqs, c->p.n.new_match_len_freqs };
			u32 v[NOPT_NSTATS];
			size_t k = 0;

			STATIC_ASSERT(ARRAY_LEN(c->p.n.match_len_freqs) == DEFLATE_MAX_MATCH_LEN + 1);
			STATIC_ASSERT(ARRAY_LEN(c->p.n.prev_observations) == NUM_OBSERVATION_TYPES);
			if (op == 'S') {
				if ((n - pos) / 4 < NOPT_NSTATS)
					die("dump-nopt: truncated stats");
				for (size_t i = 0; i < NOPT_NSTATS; i++)
					v[i] = le32(in + pos + 4 * i);
				pos += 4 * NOPT_NSTATS;
			}
			for (int f = 0; f < 6; f++)
				for (int i = 0; i < ((f & 1) ? 1 : NUM_OBSERVATION_TYPES); i++, k++)
					if (op == 'S')
						fields[f][i] = v[k];
					else
						v[k] = fields[f][i];
			for (int f = 0; f < 2; f++)
				for (int i = 0; i <= DEFLATE_MAX_MATCH_LEN; i++, k++)
					if (op == 'S')
						freqs[f][i] = v[k];
					else
						v[k] = freqs[f][i];
			if (op == 'T')
				put_u32s(v, NOPT_NSTATS);
			break;
		}
		case 'i':
			deflate_near_optimal_init_stats(c);
			break;
		case 'm':
			deflate_near_optimal_merge_stats(c);
			break;
		case 's':
			deflate_near_optimal_save_stats(c);
			break;
		case 'x':
			deflate_near_optimal_clear_old_stats(c);
			break;
		case 'l':
		case 'I': {
			unsigned first = 0;

			if (op == 'I') {
				if (n - pos < 1 || in[pos] > 1)
					die("dump-nopt: bad first flag");
				first = in[pos++];
			}
			if (n - pos < 4 || n - pos - 4 < le32(in + pos))
				die("dump-nopt: truncated data");
			bl = le32(in + pos);
			pos += 4;
			if (op == 'l') {
				deflate_choose_default_litlen_costs(c, in + pos, bl, &a, &b);
				put_u32s(&a, 1);
				put_u32s(&b, 1);
			} else {
				deflate_set_initial_costs(c, in + pos, bl, first);
			}
			pos += bl;
			break;
		}
		case 't':
			if ((n - pos) / 4 < DEFLATE_NUM_LITLEN_SYMS + DEFLATE_NUM_OFFSET_SYMS)
				die("dump-nopt: truncated freqs");
			for (u32 i = 0; i < DEFLATE_NUM_LITLEN_SYMS; i++, pos += 4)
				c->freqs.litlen[i] = le32(in + pos);
			for (u32 i = 0; i < DEFLATE_NUM_OFFSET_SYMS; i++, pos += 4)
				c->freqs.offset[i] = le32(in + pos);
			deflate_make_huffman_codes(&c->freqs, &c->codes);
			a = deflate_compute_true_cost(c);
			put_u32s(&a, 1);
			write_all(c->codes.lens.litlen, DEFLATE_NUM_LITLEN_SYMS);
			write_all(c->codes.lens.offset, DEFLATE_NUM_OFFSET_SYMS);
			break;
		case 'p':
			if (n - pos < 4)
				die("dump-nopt: truncated op");
			bl = le32(in + pos);
			pos += 4;
			ncache = nopt_read_cache(c, in, n, &pos, bl);
			nopt_init_beyond(c, bl);
			deflate_find_min_cost_path(c, bl, c->p.n.match_cache + ncache);
			for (u32 i = 0; i < bl; i++) {
				put_u32s(&c->p.n.optimum_nodes[i].cost_to_end, 1);
				put_u32s(&c->p.n.optimum_nodes[i].item, 1);
			}
			put_u32s(c->freqs.litlen, DEFLATE_NUM_LITLEN_SYMS);
			put_u32s(c->freqs.offset, DEFLATE_NUM_OFFSET_SYMS);
			write_all(c->codes.lens.litlen, DEFLATE_NUM_LITLEN_SYMS);
			write_all(c->codes.lens.offset, DEFLATE_NUM_OFFSET_SYMS);
			break;
		case 'o': {
			struct deflate_output_bitstream os;
			const unsigned char *data;
			unsigned first, final;
			u32 cap;
			bool used = false;
			unsigned char hdr[15];

			if (n - pos < 10)
				die("dump-nopt: truncated op");
			first = in[pos];
			final = in[pos + 1];
			cap = le32(in + pos + 2);
			bl = le32(in + pos + 6);
			pos += 10;
			if (first > 1 || final > 1 || cap > DF_MAX_CAP || bl < 1 ||
			    bl > MAX_BLOCK_LENGTH || n - pos < bl)
				die("dump-nopt: bad optimize op");
			data = in + pos;
			pos += bl;
			ncache = nopt_read_cache(c, in, n, &pos, bl);
			if (!buf)
				buf = xmalloc(DF_MAX_CAP);
			memset(buf, 0xA5, cap);
			os.bitbuf = 0;
			os.bitcount = 0;
			os.next = buf;
			os.end = buf + cap;
			os.overflow = false;
			deflate_optimize_and_flush_block(c, &os, data, bl,
							 c->p.n.match_cache + ncache,
							 first, final, &used);
			a = (u32)(os.next - buf);
			hdr[0] = used;
			for (int k = 0; k < 4; k++)
				hdr[1 + k] = (unsigned char)(a >> (8 * k));
			hdr[5] = (unsigned char)os.bitcount;
			for (int k = 0; k < 8; k++)
				hdr[6 + k] = (unsigned char)((uint64_t)os.bitbuf >> (8 * k));
			hdr[14] = os.overflow;
			write_all(hdr, sizeof(hdr));
			write_all(buf, a);
			nopt_put_costs(&c->p.n.costs);
			break;
		}
		default:
			die("dump-nopt: unknown op");
		}
	}
	return 0;
}

static const struct {
	const char *name;
	int (*fn)(int, char **);
} cmds[] = {
	{ "compress", cmd_compress },
	{ "decompress", cmd_decompress },
	{ "decompress-ex", cmd_decompress_ex },
	{ "decompress-batch", cmd_decompress_batch },
	{ "dump-tables", cmd_dump_tables },
	{ "dump-codes", cmd_dump_codes },
	{ "dump-header", cmd_dump_header },
	{ "dump-static", cmd_dump_static },
	{ "dump-bits", cmd_dump_bits },
	{ "dump-flush", cmd_dump_flush },
	{ "compress-bound", cmd_compress_bound },
	{ "dump-params", cmd_dump_params },
	{ "compress-batch", cmd_compress_batch },
	{ "dump-htmf", cmd_dump_htmf },
	{ "dump-split", cmd_dump_split },
	{ "dump-minlen", cmd_dump_minlen },
	{ "dump-hcmf", cmd_dump_hcmf },
	{ "dump-btmf", cmd_dump_btmf },
	{ "dump-nopt", cmd_dump_nopt },
	{ "version", cmd_version },
};

int main(int argc, char **argv)
{
	if (argc < 2) {
		fprintf(stderr, "usage: ldref <subcommand> [options]\n");
		return 2;
	}
	atexit(cleanup);
	for (size_t i = 0; i < sizeof(cmds) / sizeof(cmds[0]); i++)
		if (!strcmp(argv[1], cmds[i].name)) {
			int rc = cmds[i].fn(argc, argv);

			if (fflush(stdout) || ferror(stdout))
				die("write error");
			return rc;
		}
	fprintf(stderr, "ldref: unknown subcommand %s\n", argv[1]);
	return 2;
}

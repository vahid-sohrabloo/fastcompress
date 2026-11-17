#!/usr/bin/env python3
"""Draw the README speed charts from benchmark result files.

usage:
  python3 bench/plot.py zstd COMPRESS.md DECOMPRESS.md OUTDIR   (bench/compare.sh -k, -mode c and d)
  python3 bench/plot.py gzip RESULTS.md OUTDIR                  (bench/gzip/compare.sh -mode both)
  python3 bench/plot.py lz4 COMPRESS.md OUTDIR                  (bench/compare-lz4.sh -p, -mode c)
  python3 bench/plot.py lz4d DECOMPRESS.md... OUTDIR            (bench/compare-lz4.sh -p, -mode d)
  python3 bench/plot.py overview ZC.md ZD.md GZ.md LC.md... -- LD.md... OUTDIR
                                                                (all codecs, one chart)
  python3 bench/plot.py headline ZC.md ZD.md GZ.md LC.md... -- LD.md... SWEEP.tsv OUTDIR
                                                                (every library against ours, README's first chart)
  python3 bench/plot.py frontier SWEEP.tsv OUTDIR                (speed vs ratio, every level, all codecs)

Compression is drawn as speed against compression ratio on one input
(silesia.tar in independent blocks), one point per level and library: the
libraries do not produce the same output and their levels are their own, so
speed alone would not be a fair comparison; speed and ratio together are.

Decompression is drawn as a geometric mean of % of C's speed per level over
every row. Every library decodes the same compressed data (C's, which ours
reproduces byte for byte), so the bars compare like with like.
"""
import math
import re
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402
import matplotlib.ticker  # noqa: E402

COLORS = {"C": "#6b7280", "ours": "#2563eb", "stdlib": "#16a34a", "klauspost": "#ea580c",
          "pierrec": "#9333ea"}
MARKERS = {
    "C": dict(marker="o", markersize=11, fillstyle="none", markeredgewidth=2),
    "ours": dict(marker="o", markersize=6),
    "stdlib": dict(marker="^", markersize=7),
    "klauspost": dict(marker="s", markersize=6),
    "pierrec": dict(marker="D", markersize=6),
}


def cells(line):
    return [c.strip() for c in line.strip().strip("|").split("|")]


def num(s):
    try:
        return float(s)
    except ValueError:
        return None


def zstd_rows(path):
    out = []
    for line in open(path):
        c = cells(line)
        if len(c) < 11 or not c[1].isdigit():
            continue
        out.append(dict(file=c[0], level=int(c[1]), block=c[2],
                        C=num(c[3]), ours=num(c[4]), kp=num(c[8]),
                        rC=num(c[6]), rours=num(c[6]), rkp=num(c[10])))
    return out


def gzip_rows(path):
    """Rows of the "## Compression" and "## Decompression" tables."""
    out = {"c": [], "d": []}
    mode = None
    for line in open(path):
        if line.startswith("## "):
            mode = {"## Compression": "c", "## Decompression": "d"}.get(line.strip())
            continue
        c = cells(line)
        if mode is None or len(c) < 12 or not c[1].isdigit():
            continue
        out[mode].append(dict(file=c[0], level=int(c[1]), block=c[2],
                              C=num(c[3]), ours=num(c[4]), std=num(c[8]), kp=num(c[9]),
                              rC=num(c[6]), rours=num(c[6]), rstd=num(c[10]), rkp=num(c[11])))
    return out


def lz4_rows(path):
    """Rows of a bench/compare-lz4.sh -p table."""
    out = []
    for line in open(path):
        c = cells(line)
        if len(c) < 10 or not c[1].lstrip("-").isdigit():
            continue
        out.append(dict(file=c[0], level=int(c[1]), C=num(c[2]), ours=num(c[3]), p=num(c[7]),
                        rC=num(c[5]), rours=num(c[6]), rp=num(c[9])))
    return out


def style(ax):
    for s in ("top", "right"):
        ax.spines[s].set_visible(False)
    ax.grid(True, color="#e5e7eb", linewidth=0.8)
    ax.set_axisbelow(True)


def geomean(xs):
    return math.exp(sum(math.log(x) for x in xs) / len(xs))


def speed_ratio_chart(rows, series, title, out):
    """series: (name, label, speed key, ratio key)."""
    fig, ax = plt.subplots(figsize=(7, 4.2))
    lo, hi = float("inf"), 0
    for name, label, sk, rk in series:
        pts = sorted(((r[rk], r[sk], r["level"]) for r in rows if r[sk] and r[rk]), key=lambda p: p[2])
        if not pts:
            continue
        lo, hi = min(lo, *(p[1] for p in pts)), max(hi, *(p[1] for p in pts))
        ax.plot([p[0] for p in pts], [p[1] for p in pts], color=COLORS[name], linewidth=1.5,
                label=label, **MARKERS[name])
        if name != "C":
            for x, y, l in pts:
                ax.annotate(f"L{l}", (x, y), textcoords="offset points", xytext=(7, 4),
                            fontsize=8, color=COLORS[name])
    ax.set_yscale("log")
    ticks = [t for t in (1, 2, 3, 5, 10, 15, 20, 30, 50, 75, 100, 150, 200, 300, 500, 1000)
             if lo / 1.5 <= t <= hi * 1.5]
    ax.set_yticks(ticks, [str(t) for t in ticks])
    ax.yaxis.set_minor_formatter(matplotlib.ticker.NullFormatter())
    ax.set_xlabel("compression ratio (higher is better)")
    ax.set_ylabel("compression speed, MB/s (log)")
    ax.set_title(title, fontsize=11)
    style(ax)
    ax.legend(frameon=False)
    fig.tight_layout()
    fig.savefig(out, facecolor="white")
    plt.close(fig)


def pct_bars_chart(rows, levels, series, title, out):
    """series: (name, label, speed key); bars are geomean % of C per level."""
    fig, ax = plt.subplots(figsize=(7, 4.2))
    n = len(series)
    w = 0.8 / n
    for i, (name, label, sk) in enumerate(series):
        xs, vals = [], []
        for j, l in enumerate(levels):
            ps = [100 * r[sk] / r["C"] for r in rows if r["level"] == l and r[sk] and r["C"]]
            if ps:
                xs.append(j + (i - (n - 1) / 2) * w)
                vals.append(geomean(ps))
        bars = ax.bar(xs, vals, w, color=COLORS[name], label=label)
        for b, v in zip(bars, vals):
            ax.annotate(f"{v:.0f}", (b.get_x() + b.get_width() / 2, v), textcoords="offset points",
                        xytext=(0, 2), ha="center", fontsize=8)
    ax.set_xticks(range(len(levels)), [f"level {l}" for l in levels])
    ax.set_ylim(0, 120)
    ax.set_ylabel("speed, % of C (geometric mean)")
    ax.set_title(title, fontsize=11)
    style(ax)
    ax.grid(False, axis="x")
    ax.legend(frameon=False, loc="upper center", bbox_to_anchor=(0.5, -0.1), ncol=n if n <= 3 else 2)
    fig.tight_layout()
    fig.savefig(out, facecolor="white")
    plt.close(fig)


def zstd(cpath, dpath, outdir):
    c = [r for r in zstd_rows(cpath) if r["file"] == "silesia.tar" and r["block"] == "4M"]
    speed_ratio_chart(c, [("C", "C zstd 1.5.7", "C", "rC"),
                          ("ours", "fastcompress", "ours", "rours"),
                          ("klauspost", "klauspost/compress", "kp", "rkp")],
                      "zstd compression: speed vs ratio (silesia.tar, 4 MiB blocks)",
                      f"{outdir}/zstd-compress.svg")
    d = zstd_rows(dpath)
    pct_bars_chart(d, [1, 3, 7, 11], [("C", "C zstd 1.5.7", "C"),
                                      ("ours", "fastcompress", "ours"),
                                      ("klauspost", "klauspost/compress", "kp")],
                   "zstd decompression: every library decodes the same frames",
                   f"{outdir}/zstd-decompress.svg")


def gzip(path, outdir):
    rs = gzip_rows(path)
    c = [r for r in rs["c"] if r["file"] == "silesia.tar" and r["block"] == "1M"]
    speed_ratio_chart(c, [("C", "libdeflate 1.26", "C", "rC"),
                          ("ours", "fastcompress", "ours", "rours"),
                          ("stdlib", "Go compress/flate", "std", "rstd"),
                          ("klauspost", "klauspost/compress", "kp", "rkp")],
                      "DEFLATE compression: speed vs ratio (silesia.tar, 1 MiB blocks)",
                      f"{outdir}/deflate-compress.svg")
    levels = sorted({r["level"] for r in rs["d"]})
    pct_bars_chart(rs["d"], levels, [("C", "libdeflate 1.26", "C"),
                                     ("ours", "fastcompress", "ours"),
                                     ("stdlib", "Go compress/flate", "std"),
                                     ("klauspost", "klauspost/compress", "kp")],
                   "DEFLATE decompression: every library decodes the same streams",
                   f"{outdir}/deflate-decompress.svg")


def lz4(cpath, outdir):
    c = lz4_rows(cpath)
    files = sorted({r["file"] for r in c})
    if len(files) != 1:
        sys.exit(f"{cpath}: want rows of one file, got {files}")
    m = re.search(r" -B(\d+[KM]?)\b", open(cpath).read())
    blocks = {"64K": "64 KiB", "256K": "256 KiB", "1M": "1 MiB", "4M": "4 MiB"}.get(m.group(1), m.group(1)) if m else "?"
    speed_ratio_chart(c, [("C", "C lz4 1.11.1", "C", "rC"),
                          ("ours", "fastcompress", "ours", "rours"),
                          ("pierrec", "pierrec/lz4", "p", "rp")],
                      f"LZ4 compression: speed vs ratio ({files[0]}, {blocks} blocks)",
                      f"{outdir}/lz4-compress.svg")


def lz4d(paths, outdir):
    d = [r for p in paths for r in lz4_rows(p)]
    pct_bars_chart(d, sorted({r["level"] for r in d}), [("C", "C lz4 1.11.1", "C"),
                                                       ("ours", "fastcompress", "ours"),
                                                       ("pierrec", "pierrec/lz4", "p")],
                   "LZ4 decompression: every library decodes the same frames",
                   f"{outdir}/lz4-decompress.svg")


def overview(zc, zd, gz, lc, ld, outdir):
    """One chart: geometric mean of % of C's speed over every row, per codec.
    Compression (left) shows ours only: our output is C's, so speed is the
    whole difference. The other Go libraries produce other output at their
    level numbers, so their compression speed is only comparable together
    with ratio (the per-codec speed-vs-ratio charts), not as a bar here.
    Decompression (right) shows every library: all decode the same C-made
    data."""
    zc, zd = zstd_rows(zc), zstd_rows(zd)
    g = gzip_rows(gz)
    gc = [r for r in g["c"] if r["std"]]  # the Go libraries have no levels 10-12
    gcall, gd = g["c"], [r for r in g["d"] if r["std"]]
    lc = [r for p in lc for r in lz4_rows(p)]
    ld = [r for p in ld for r in lz4_rows(p)]
    pct = lambda rows, k: geomean([100 * r[k] / r["C"] for r in rows if r[k] and r["C"]])
    size = lambda rows, rk: 100 * (geomean([r["rC"] / r[rk] for r in rows if r[rk]]) - 1)
    groups = [
        ("zstd", [("ours", pct(zc, "ours"), None)],
                 [("ours", pct(zd, "ours")), ("klauspost", pct(zd, "kp"))]),
        ("DEFLATE", [("ours", pct(gcall, "ours"), None)],
                    [("ours", pct(gd, "ours")), ("stdlib", pct(gd, "std")), ("klauspost", pct(gd, "kp"))]),
        ("LZ4", [("ours", pct(lc, "ours"), None)],
                [("ours", pct(ld, "ours")), ("pierrec", pct(ld, "p"))]),
    ]
    labels = {"ours": "fastcompress", "klauspost": "klauspost/compress",
              "stdlib": "Go compress/flate", "pierrec": "pierrec/lz4"}
    fig, axes = plt.subplots(1, 2, figsize=(10, 4.4), sharey=True, gridspec_kw={"width_ratios": [1, 2]})
    seen = set()
    for ax, which, title in ((axes[0], 1, "compression (output identical to C's)"),
                             (axes[1], 2, "decompression (every library decodes the same data)")):
        x = 0
        ticks = []
        for name, comp, dec in groups:
            bars = comp if which == 1 else [(n, v, None) for n, v in dec]
            start = x
            for lib, v, sz in bars:
                lab = labels[lib] if lib not in seen else None
                seen.add(lib)
                ax.bar(x, v, 0.8, color=COLORS[lib], label=lab)
                ax.annotate(f"{v:.0f}%", (x, v), textcoords="offset points", xytext=(0, 2),
                            ha="center", fontsize=8, fontweight="bold" if lib == "ours" else None)
                if sz is not None:
                    ax.annotate(f"size\n{sz:+.0f}%", (x, 0), textcoords="offset points", xytext=(0, 3),
                                ha="center", fontsize=7, color="white")
                x += 1
            ticks.append(((start + x - 1) / 2, name))
            x += 0.8
        ax.axhline(100, color=COLORS["C"], linewidth=1.2, linestyle="--")
        ax.annotate("C = 100%", (x - 0.9, 100), textcoords="offset points", xytext=(0, 3),
                    ha="right", fontsize=8, color=COLORS["C"])
        ax.set_xticks([t for t, _ in ticks], [n for _, n in ticks])
        ax.set_title(title, fontsize=11)
        style(ax)
        ax.grid(False, axis="x")
    axes[0].set_ylabel("speed, % of the C library (geometric mean)")
    axes[0].set_ylim(0, 125)
    fig.legend(frameon=False, loc="lower center", ncol=4, bbox_to_anchor=(0.5, -0.01))
    fig.suptitle("Speed against the C library each package ports (one core, higher is better)", fontsize=11)
    fig.tight_layout(rect=(0, 0.07, 1, 1))
    fig.savefig(f"{outdir}/overview.svg", facecolor="white")
    plt.close(fig)


def ours_speed_at(ours, r):
    """Our speed at ratio r along our level curve (ratio, MB/s), interpolated
    log-linearly between the two levels around r; below our lowest ratio,
    our fastest level (which then compresses better than r)."""
    ours = sorted(ours)
    if r <= ours[0][0]:
        return max(o[1] for o in ours)
    for (r0, v0, _), (r1, v1, _) in zip(ours, ours[1:]):
        if r0 <= r <= r1:
            t = 0 if r1 == r0 else (r - r0) / (r1 - r0)
            return math.exp(math.log(v0) + t * (math.log(v1) - math.log(v0)))
    return float("nan")


def frontier(path, outdir):
    """Speed against compression ratio for every library at every level, one
    panel per codec, from a tab-separated sweep (codec, lib, level, MB/s,
    ratio; the best of repeated runs is kept). For each point of another
    library, the label gives our speed at the same ratio (on our level curve)
    divided by its speed."""
    best = {}
    for line in open(path):
        f = line.rstrip("\n").split("\t")
        if len(f) < 5 or not num(f[3]) or not num(f[4]):
            continue
        k = (f[0], f[1], int(f[2]))
        if k not in best or num(f[3]) > best[k][0]:
            best[k] = (num(f[3]), num(f[4]))
    panels = [("zstd", "zstd (silesia.tar, 4 MiB blocks)", ["ours", "klauspost"]),
              ("deflate", "DEFLATE (silesia.tar, 1 MiB blocks)", ["ours", "stdlib", "klauspost"]),
              ("lz4", "LZ4 (silesia.tar, 64 KiB blocks)", ["ours", "pierrec"])]
    labels = {"ours": "fastcompress (C's output)", "klauspost": "klauspost/compress",
              "stdlib": "Go compress/flate", "pierrec": "pierrec/lz4"}
    fig, axes = plt.subplots(1, 3, figsize=(13, 4.6))
    seen, speedups = set(), {}
    for codec, title, libs in panels:
        f1, a1 = plt.subplots(figsize=(7, 4.2))
        frontier_panel(a1, best, codec, libs, labels, set(), {})
        a1.set_ylabel("compression speed, MB/s (log)")
        a1.set_title(f"{title}: compression speed vs ratio, every level\n"
                     "labels: our speed at the same ratio / theirs", fontsize=10)
        a1.legend(frameon=False, fontsize=8)
        f1.tight_layout()
        f1.savefig(f"{outdir}/{codec}-compress.svg", facecolor="white")
        plt.close(f1)
    for ax, (codec, title, libs) in zip(axes, panels):
        frontier_panel(ax, best, codec, libs, labels, seen, speedups)
        ax.set_title(title, fontsize=10)
    axes[0].set_ylabel("compression speed, MB/s (log)")
    fig.legend(frameon=False, loc="lower center", ncol=4, bbox_to_anchor=(0.5, -0.01))
    fig.suptitle("Compression, every level: further up and right is better. "
                 "Labels: our speed at the same ratio, divided by theirs", fontsize=10)
    fig.tight_layout(rect=(0, 0.07, 1, 0.96))
    fig.savefig(f"{outdir}/frontier.svg", facecolor="white")
    plt.close(fig)
    for k, xs in sorted(speedups.items()):
        print(k, "min %.2fx geomean %.2fx max %.2fx (n=%d)" % (min(xs), geomean(xs), max(xs), len(xs)))


def frontier_panel(ax, best, codec, libs, labels, seen, speedups):
    if True:
        ours = sorted((v[1], v[0], k[2]) for k, v in best.items() if k[:2] == (codec, "ours"))
        lo, hi = float("inf"), 0
        for lib in libs:
            pts = sorted(((v[1], v[0], k[2]) for k, v in best.items() if k[:2] == (codec, lib)), key=lambda p: p[0])
            # Drop a library's own dominated points (a level that is slower than
            # another of its levels without compressing better): they would
            # only make that library look worse.
            pts = [p for p in pts if not any(q[0] >= p[0] and q[1] > p[1] for q in pts if q is not p)]
            if not pts:
                continue
            lo, hi = min(lo, *(p[1] for p in pts)), max(hi, *(p[1] for p in pts))
            lab = labels[lib] if lib not in seen else None
            seen.add(lib)
            ax.plot([p[0] for p in pts], [p[1] for p in pts], color=COLORS[lib], linewidth=2 if lib == "ours" else 1.3,
                    label=lab, **MARKERS[lib])
            if lib == "ours":
                continue
            for r, v, lvl in pts:
                x = ours_speed_at(ours, r) / v
                speedups.setdefault((codec, lib), []).append(x)
                ax.annotate(f"{x:.1f}x", (r, v), textcoords="offset points", xytext=(5, -10),
                            fontsize=7, color=COLORS[lib])
        ax.set_yscale("log")
        ticks = [t for t in (1, 2, 3, 5, 10, 20, 30, 50, 100, 200, 300, 500, 1000, 2000) if lo / 1.5 <= t <= hi * 1.5]
        ax.set_yticks(ticks, [str(t) for t in ticks])
        ax.yaxis.set_minor_formatter(matplotlib.ticker.NullFormatter())
        ax.set_xlabel("compression ratio (higher is better)")
        style(ax)


def headline(zc, zd, gz, lc, ld, sweep, outdir):
    """The README's first chart: every library's speed as a percentage of
    ours (ours = 100%). Compression: C's speed against ours on the same
    output (ours is byte-identical), and each other Go library's speed
    against ours at the same compression ratio (bench/sweep.sh, every
    level, geometric mean over its levels). Decompression: every library
    decodes the same C-made data."""
    zc, zd = zstd_rows(zc), zstd_rows(zd)
    g = gzip_rows(gz)
    gd = [r for r in g["d"] if r["std"]]
    lc = [r for p in lc for r in lz4_rows(p)]
    ld = [r for p in ld for r in lz4_rows(p)]
    rel = lambda rows, k: geomean([100 * r[k] / r["ours"] for r in rows if r[k] and r["ours"]])
    best = {}
    for line in open(sweep):
        f = line.rstrip("\n").split("\t")
        if len(f) >= 5 and num(f[3]) and num(f[4]):
            k = (f[0], f[1], int(f[2]))
            if k not in best or num(f[3]) > best[k][0]:
                best[k] = (num(f[3]), num(f[4]))
    def same_ratio(codec, lib):
        ours = [(v[1], v[0], k[2]) for k, v in best.items() if k[:2] == (codec, "ours")]
        pts = [(v[1], v[0]) for k, v in best.items() if k[:2] == (codec, lib)]
        pts = [p for p in pts if not any(q[0] >= p[0] and q[1] > p[1] for q in pts if q is not p)]
        return geomean([100 * v / ours_speed_at(ours, r) for r, v in pts])
    comp = [("zstd", [("C", rel(zc, "C")), ("klauspost", same_ratio("zstd", "klauspost"))]),
            ("DEFLATE", [("C", rel(g["c"], "C")), ("klauspost", same_ratio("deflate", "klauspost")),
                         ("stdlib", same_ratio("deflate", "stdlib"))]),
            ("LZ4", [("C", rel(lc, "C")), ("pierrec", same_ratio("lz4", "pierrec"))])]
    dec = [("zstd", [("C", rel(zd, "C")), ("klauspost", rel(zd, "kp"))]),
           ("DEFLATE", [("C", rel(gd, "C")), ("klauspost", rel(gd, "kp")), ("stdlib", rel(gd, "std"))]),
           ("LZ4", [("C", rel(ld, "C")), ("pierrec", rel(ld, "p"))])]
    labels = {"ours": "fastcompress", "C": "the C library", "klauspost": "klauspost/compress",
              "stdlib": "Go compress/flate", "pierrec": "pierrec/lz4"}
    fig, axes = plt.subplots(1, 2, figsize=(11, 4.6), sharey=True)
    seen = set()
    for ax, groups, title in ((axes[0], comp, "compression (at the same output size)"),
                              (axes[1], dec, "decompression (the same data)")):
        x, ticks = 0, []
        for name, bars in groups:
            start = x
            for lib, v in [("ours", 100.0)] + bars:
                ax.bar(x, v, 0.8, color=COLORS[lib], label=labels[lib] if lib not in seen else None)
                seen.add(lib)
                ax.annotate(f"{v:.0f}%", (x, v), textcoords="offset points", xytext=(0, 2),
                            ha="center", fontsize=8, fontweight="bold" if lib == "ours" else None)
                x += 1
            ticks.append(((start + x - 1) / 2, name))
            x += 0.8
        ax.axhline(100, color=COLORS["ours"], linewidth=1, linestyle="--")
        ax.set_xticks([t for t, _ in ticks], [n for _, n in ticks])
        ax.set_title(title, fontsize=11)
        style(ax)
        ax.grid(False, axis="x")
    axes[0].set_ylabel("speed, % of fastcompress (higher is faster)")
    axes[0].set_ylim(0, 122)
    fig.legend(frameon=False, loc="lower center", ncol=5, bbox_to_anchor=(0.5, -0.01))
    fig.suptitle("Every library's speed against fastcompress (one core; below the line is slower)", fontsize=11)
    fig.tight_layout(rect=(0, 0.07, 1, 1))
    fig.savefig(f"{outdir}/headline.svg", facecolor="white")
    plt.close(fig)
    for n, b in comp:
        print("comp", n, [(l, round(v, 1)) for l, v in b])
    for n, b in dec:
        print("dec ", n, [(l, round(v, 1)) for l, v in b])


def main():
    a = sys.argv[1:]
    if len(a) == 4 and a[0] == "zstd":
        zstd(*a[1:])
    elif len(a) == 3 and a[0] == "gzip":
        gzip(*a[1:])
    elif len(a) == 3 and a[0] == "lz4":
        lz4(*a[1:])
    elif len(a) >= 9 and a[0] == "headline" and "--" in a:
        i = a.index("--")
        headline(a[1], a[2], a[3], a[4:i], a[i + 1:-2], a[-2], a[-1])
    elif len(a) == 3 and a[0] == "frontier":
        frontier(*a[1:])
    elif len(a) >= 3 and a[0] == "lz4d":
        lz4d(a[1:-1], a[-1])
    elif len(a) >= 8 and a[0] == "overview" and "--" in a:
        i = a.index("--")
        overview(a[1], a[2], a[3], a[4:i], a[i + 1:-1], a[-1])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()

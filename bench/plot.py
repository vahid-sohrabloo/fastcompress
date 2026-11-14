#!/usr/bin/env python3
"""Draw the README speed charts from benchmark result files.

usage:
  python3 bench/plot.py zstd COMPRESS.md DECOMPRESS.md OUTDIR   (bench/compare.sh -k, -mode c and d)
  python3 bench/plot.py gzip RESULTS.md OUTDIR                  (bench/gzip/compare.sh -mode both)

Compression is drawn as speed against compression ratio on one input
(silesia.tar in independent blocks), one point per level and library: the
libraries do not produce the same output and their levels are their own, so
speed alone would not be a fair comparison; speed and ratio together are.

Decompression is drawn as a geometric mean of % of C's speed per level over
every row. Every library decodes the same compressed data (C's, which ours
reproduces byte for byte), so the bars compare like with like.
"""
import math
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402
import matplotlib.ticker  # noqa: E402

COLORS = {"C": "#6b7280", "ours": "#2563eb", "stdlib": "#16a34a", "klauspost": "#ea580c"}
MARKERS = {
    "C": dict(marker="o", markersize=11, fillstyle="none", markeredgewidth=2),
    "ours": dict(marker="o", markersize=6),
    "stdlib": dict(marker="^", markersize=7),
    "klauspost": dict(marker="s", markersize=6),
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


def main():
    a = sys.argv[1:]
    if len(a) == 4 and a[0] == "zstd":
        zstd(*a[1:])
    elif len(a) == 3 and a[0] == "gzip":
        gzip(*a[1:])
    else:
        sys.exit(__doc__)


if __name__ == "__main__":
    main()

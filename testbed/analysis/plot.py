"""Paper figures from analyze.py's summary.csv (matplotlib, IEEE single-column width).

    python plot.py <summary.csv> <out-dir>

Writes, as PDF and PNG:
  recovery_cdf_<fault>.pdf  CDF of HTTP (Service) recovery time per configuration
  recovery_bars.pdf         median recovery per fault and configuration, p95 whisker
  availability.pdf          share of successful Service probes during the fault
  mqtt_delivery_q0.pdf      QoS 0 delivery ratio per fault and configuration
  mqtt_gap.pdf              longest MQTT delivery gap per fault and configuration
Every series has a fixed color, a legend entry and its own line style or hatch, so the
figures read in grayscale print too. Runs that never recovered during the fault are
plotted at the fault duration and marked as censored.
"""
import csv
import os
import sys
from collections import defaultdict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

# Fixed order, fixed color: a configuration keeps its color in every figure.
CONFIGS = [
    ("vanilla", "Kubernetes", "#257de0", "-", ""),
    ("node", "Node failover", "#f47230", "--", "//"),
    ("full", "Link steering", "#00b894", "-.", "\\\\"),
    ("full-norelay", "Steering, no relay", "#f2a600", ":", "xx"),
]
FAULTS = [
    ("link-cut", "Link cut"),
    ("link-cut-both", "Both links cut"),
    ("link-loss30", "30% loss"),
    ("link-flap", "Flapping"),
    ("overlay-cut", "Overlay fault"),
    ("node-cni", "Node CNI fault"),
]
INK = "#1f1f1f"
MUTED = "#6b6b6b"
GRID = "#e4e4e4"

plt.rcParams.update({
    "font.size": 7.5, "axes.titlesize": 8, "axes.labelsize": 7.5, "legend.fontsize": 7,
    "xtick.labelsize": 7, "ytick.labelsize": 7, "axes.edgecolor": MUTED,
    "axes.labelcolor": INK, "xtick.color": MUTED, "ytick.color": MUTED, "text.color": INK,
    "axes.spines.top": False, "axes.spines.right": False, "axes.grid": True,
    "grid.color": GRID, "grid.linewidth": 0.6, "axes.axisbelow": True,
    "pdf.fonttype": 42, "ps.fonttype": 42, "savefig.bbox": "tight", "savefig.dpi": 300,
})
WIDTH = 3.5  # inches, IEEE column


def load(path):
    with open(path, newline="") as f:
        return list(csv.DictReader(f))


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def percentile(vals, p):
    s = sorted(vals)
    if not s:
        return None
    k = max(0, min(len(s) - 1, int(round(p / 100.0 * len(s) + 0.5)) - 1))
    return s[k]


def groups(rows, metric):
    out = defaultdict(list)
    for r in rows:
        v = num(r.get(metric))
        if v is not None:
            out[(r["config"], r["fault"])].append(v)
    return out


def present(rows):
    cs = {r["config"] for r in rows}
    fs = {r["fault"] for r in rows}
    return [c for c in CONFIGS if c[0] in cs], [f for f in FAULTS if f[0] in fs]


def save(fig, out, name):
    for ext in ("pdf", "png"):
        fig.savefig(os.path.join(out, "%s.%s" % (name, ext)))
    plt.close(fig)


def recovery_cdf(rows, out, fault, label):
    data = groups(rows, "http_svc_recovery_s")
    censored = defaultdict(int)
    for r in rows:
        if r["fault"] == fault and r.get("http_svc_recovered") == "0":
            censored[r["config"]] += 1
    configs, _ = present(rows)
    fig, ax = plt.subplots(figsize=(WIDTH, 1.9))
    xmax = 0
    for key, name, color, ls, _ in configs:
        vals = sorted(data.get((key, fault), []))
        if not vals:
            continue
        xmax = max(xmax, vals[-1])
        ys = [(i + 1) / len(vals) for i in range(len(vals))]
        ax.step([0] + vals, [0] + ys, where="post", color=color, linestyle=ls, linewidth=1.4,
                label=name + (" (no recovery)" if censored[key] == len(vals) else ""))
    ax.set_xlim(0, xmax * 1.05 if xmax else 1)
    ax.set_ylim(0, 1.02)
    ax.set_xlabel("Time from fault to recovery (s)")
    ax.set_ylabel("Fraction of runs")
    ax.set_title("Service recovery: %s" % label, loc="left")
    ax.legend(frameon=False, loc="center")
    save(fig, out, "recovery_cdf_%s" % fault)


def grouped_bars(rows, out, metric, ylabel, title, name, scale=1.0, ylim=None, tail=95,
                 only=None):
    """Median bar with a whisker to the bad tail: p95 for times, p5 for ratios."""
    data = groups(rows, metric)
    configs, faults = present(rows)
    if only:
        faults = [f for f in faults if f[0] in only]
    n = len(configs)
    width = 0.8 / max(n, 1)
    fig, ax = plt.subplots(figsize=(WIDTH, 2.0))
    # Each fault's group is centred on the configurations that have data for it.
    slots = {}
    for j, (fkey, _) in enumerate(faults):
        have = [c[0] for c in configs if data.get((c[0], fkey))]
        for i, key in enumerate(have):
            slots[(key, fkey)] = j + width * (i - (len(have) - 1) / 2.0)
    zeros = []
    for key, cname, color, _, hatch in configs:
        xs, meds, errs = [], [], [[], []]
        for fkey, _ in faults:
            vals = [v * scale for v in data.get((key, fkey), [])]
            if not vals:
                continue
            med = percentile(vals, 50)
            worst = percentile(vals, tail)
            xs.append(slots[(key, fkey)])
            meds.append(med)
            errs[0].append(max(0.0, med - worst))
            errs[1].append(max(0.0, worst - med))
            if med < 0.5:  # in plotted units: under 0.5 % or 0.5 s
                zeros.append(slots[(key, fkey)])
        # The edge line in the surface color leaves a visible gap between adjacent bars.
        ax.bar(xs, meds, width=width, color=color, hatch=hatch, edgecolor="white",
               linewidth=0.8, label=cname, yerr=errs, error_kw={"elinewidth": 0.7,
               "capsize": 1.5, "ecolor": MUTED})
    # A bar of height zero is invisible: label it, so it does not read as missing data.
    for x in zeros:
        ax.annotate("0", (x, 0), xytext=(0, 1.5), textcoords="offset points",
                    ha="center", va="bottom", fontsize=5.5, color=MUTED)
    ax.set_xticks(range(len(faults)))
    ax.set_xticklabels([f[1] for f in faults], rotation=20, ha="right")
    ax.set_ylabel(ylabel)
    if ylim:
        ax.set_ylim(*ylim)
    ax.grid(axis="x", visible=False)
    ax.set_title(title, loc="left")
    ax.legend(frameon=False, ncol=2, loc="upper left", bbox_to_anchor=(0, -0.42))
    save(fig, out, name)


def main(argv):
    if len(argv) != 3:
        print(__doc__)
        return 2
    rows = load(argv[1])
    out = argv[2]
    os.makedirs(out, exist_ok=True)
    _, faults = present(rows)
    for fkey, flabel in faults:
        recovery_cdf(rows, out, fkey, flabel)
    grouped_bars(rows, out, "http_svc_recovery_s", "Recovery time (s)",
                 "Service recovery (median, p95 whisker)", "recovery_bars",
                 only=("link-cut", "link-cut-both", "overlay-cut", "node-cni"))
    grouped_bars(rows, out, "http_svc_avail", "Successful probes (%)",
                 "Service availability during the fault (median, p5 whisker)",
                 "availability", scale=100.0, ylim=(0, 105), tail=5)
    grouped_bars(rows, out, "mqtt_q0_delivery", "Delivered (%)",
                 "MQTT QoS 0 delivery (median, p5 whisker)", "mqtt_delivery_q0",
                 scale=100.0, ylim=(0, 105), tail=5)
    grouped_bars(rows, out, "mqtt_q1_max_gap_s", "Longest gap (s)",
                 "MQTT QoS 1 longest delivery gap (median, p95 whisker)", "mqtt_gap")
    print("figures written to", out)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))

"""Print every number the paper's text quotes, from a campaign-all.sh results directory.

    python3 paper_numbers.py <results-dir>

Reads <dir>/main, profile-edge, sweep-d1, sweep-d4, rp-strict, pinning and nat-default
(whichever exist) through analyze.py's per-run metrics, plus the link routers' logs
for path changes. Medians and percentiles use analyze.py's definitions.
"""
import datetime
import glob
import json
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import analyze  # noqa: E402


def pct(vals, p):
    return analyze.percentile(sorted(vals), p) if vals else None


def med(vals):
    return pct(vals, 50)


def changes(run_dir, side="src"):
    """(time, from, to, via) of every path change in a router log."""
    out = []
    path = os.path.join(run_dir, "router-%s.log" % side)
    if not os.path.exists(path):
        return out
    for line in open(path):
        if "Changed path" not in line:
            continue
        ts = datetime.datetime.strptime(line.split()[0][:26] + "Z", "%Y-%m-%dT%H:%M:%S.%fZ")
        t = ts.replace(tzinfo=datetime.timezone.utc).timestamp()
        js = json.loads(line[line.index("{"):])
        out.append((t, js.get("from"), js.get("to"), js.get("via")))
    return out


def runs(campaign, config, fault):
    res = []
    for d in sorted(glob.glob(os.path.join(campaign, config, fault, "rep*"))):
        if not os.path.exists(os.path.join(d, "meta.json")):
            continue
        m = json.load(open(os.path.join(d, "meta.json")))
        r = analyze.analyze_run(d, m)
        r["_dir"], r["_meta"] = d, m
        res.append(r)
    return res


def f(x, nd=1):
    return "-" if x is None else ("%.*f" % (nd, x))


def show_cell(campaign, config, fault, label=None):
    R = runs(campaign, config, fault)
    if not R:
        return R
    rec = [r["http_svc_recovery_s"] for r in R]
    nrec = sum(1 for r in R if r.get("http_svc_recovered"))
    av = [r["http_svc_avail"] for r in R]
    q0 = [r["mqtt_q0_delivery"] for r in R if r.get("mqtt_q0_delivery") is not None]
    gap = [r["mqtt_q1_max_gap_s"] for r in R if r.get("mqtt_q1_max_gap_s") is not None]
    q1rc = sum(1 for r in R if (r.get("mqtt_q1_reconnects") or 0) > 0)
    q0rc = sum(1 for r in R if (r.get("mqtt_q0_reconnects") or 0) > 0)
    fb = [r.get("http_svc_failback_failed") or 0 for r in R]
    print("  %-13s %-13s n=%2d rec %s (p95 %s) recovered %d/%d | avail %s%% (p5 %s%%) | "
          "q0 %s%% (min %s%%) q0rc %d | q1gap %s (p95 %s, max %s) q1rc %d | failback-failed %s" % (
              config, label or fault, len(R), f(med(rec)), f(pct(rec, 95)), nrec, len(R),
              f(100 * med(av), 0), f(100 * pct(av, 5), 0), f(100 * med(q0), 0) if q0 else "-",
              f(100 * min(q0), 0) if q0 else "-", q0rc, f(med(gap)), f(pct(gap, 95)),
              f(max(gap) if gap else None), q1rc, fb))
    return R


def fault_window_changes(r, side="src"):
    m = r["_meta"]
    tf, tc, te = m["t_fault_ns"] / 1e9, m["t_clear_ns"] / 1e9, m["t_end_ns"] / 1e9
    ch = changes(r["_dir"], side)
    during = [c for c in ch if tf <= c[0] < tc]
    after = [c for c in ch if tc <= c[0] < te]
    before = [c for c in ch if m["t_start_ns"] / 1e9 <= c[0] < tf]
    return before, during, after, tf, tc


def main(d):
    main_dir = os.path.join(d, "main")
    print("== Tables II and III, Fig. 3 (main)")
    cells = {}
    for config in ("vanilla", "node", "full", "full-norelay"):
        for fault in ("link-cut", "link-cut-both", "link-loss30", "link-flap", "overlay-cut", "node-cni"):
            R = show_cell(main_dir, config, fault)
            if R:
                cells[(config, fault)] = R

    print("\n== MQTT under steering (full, link and overlay faults)")
    lf = [r for k, R in cells.items() if k[0] == "full" and k[1] != "node-cni" for r in R]
    q1 = sum(1 for r in lf if (r.get("mqtt_q1_reconnects") or 0) > 0)
    q0 = sum(1 for r in lf if (r.get("mqtt_q0_reconnects") or 0) > 0)
    dl = [r["mqtt_q0_delivery"] for r in lf]
    print("  runs %d: QoS1 reconnected in %d, QoS0 reconnected in %d; QoS0 delivery median %s%%, worst %s%%"
          % (len(lf), q1, q0, f(100 * med(dl), 0), f(100 * min(dl), 0)))
    lc = cells.get(("full", "link-cut"), [])
    gaps = sorted(round(r["mqtt_q1_max_gap_s"], 1) for r in lc)
    print("  link-cut QoS1 gaps:", gaps, "| <10 s:", sum(g < 10 for g in gaps), ">=10 s:", sum(g >= 10 for g in gaps))
    noloss = [r for r in lf if (r.get("mqtt_q0_reconnects") or 0) == 0]
    print("  QoS0 delivery in runs without QoS0 reconnection: min %s%%" % f(100 * min(r["mqtt_q0_delivery"] for r in noloss), 0))

    print("\n== Path changes (A's router)")
    for fault in ("link-cut", "link-cut-both", "overlay-cut", "link-loss30", "link-flap"):
        rows = []
        for r in cells.get(("full", fault), []):
            b, du, af, tf, tc = fault_window_changes(r)
            rows.append("%d/%d/%d[%s]" % (len(b), len(du), len(af),
                        ",".join("%s@%.1f" % (c[2], c[0] - tf) for c in du)))
        print("  %-13s before/during/after:" % fault, " ".join(rows))
    fl = cells.get(("full", "link-flap"), [])
    for n_sw in (0, 1, 2, 3):
        sel = [r for r in fl if len(fault_window_changes(r)[1]) == n_sw]
        if sel:
            print("  flap: %d runs with %d switches, availability median %s%%" % (
                len(sel), n_sw, f(100 * med([r["http_svc_avail"] for r in sel]), 0)))
    ls = cells.get(("full", "link-loss30"), [])
    st = [r["steer_s"] for r in ls if r.get("steer_s") is not None]
    print("  loss30 first switch: median %s s, p95 %s s" % (f(med(st)), f(pct(st, 95))))
    nc = cells.get(("full", "node-cni"), [])
    st = [r["steer_s"] for r in nc if r.get("steer_s") is not None]
    print("  node-cni: steering first switch median %s s" % f(med(st)))

    print("\n== Without steering")
    vq0 = [med([r["mqtt_q0_delivery"] for r in cells[("vanilla", f_)]])
           for f_ in ("link-cut", "link-cut-both", "overlay-cut", "node-cni") if ("vanilla", f_) in cells]
    print("  vanilla QoS0 delivery medians (cuts, overlay, CNI):", [f(100 * x, 0) for x in vq0])
    ng = sorted(round(r["mqtt_q1_max_gap_s"], 1) for r in cells.get(("node", "node-cni"), []))
    print("  node failover, node CNI fault: QoS1 longest gaps", ng)
    for config in ("vanilla", "node", "full"):
        R = cells.get((config, "link-loss30"), [])
        print("  loss30 %-8s QoS1 reconnect runs %d, QoS0 reconnect runs %d" % (
            config, sum(1 for r in R if (r.get("mqtt_q1_reconnects") or 0) > 0),
            sum(1 for r in R if (r.get("mqtt_q0_reconnects") or 0) > 0)))

    for name, faults in (("profile-edge", ("link-cut", "link-cut-both", "link-loss30")),
                         ("sweep-d1", ("link-cut",)), ("sweep-d4", ("link-cut",)),
                         ("rp-strict", ("link-loss30",)), ("nat-default", ("link-cut",)),
                         ("pinning", ("node-cni", "link-cut-both"))):
        c = os.path.join(d, name)
        if not os.path.isdir(c):
            continue
        print("\n== %s" % name)
        for fault in faults:
            R = show_cell(c, "full", fault)
            if name == "profile-edge" and R:
                pre = sum(len(fault_window_changes(r, s)[0]) for r in R for s in ("src", "dst"))
                once = sum(1 for r in R if len(fault_window_changes(r)[1]) == 1)
                print("    switches before fault (both routers): %d; runs with one switch during fault: %d/%d" % (pre, once, len(R)))
            if name in ("pinning", "nat-default") and R:
                print("    per run QoS1/QoS0 reconnects:", [(r.get("mqtt_q1_reconnects"), r.get("mqtt_q0_reconnects")) for r in R],
                      "gaps:", [round(r["mqtt_q1_max_gap_s"], 1) for r in R])
    for dd in (1, 2, 4):
        p = os.path.join(d, "overhead-d%d.txt" % dd)
        if os.path.exists(p):
            print("\n== overhead delta=%d" % dd)
            print(open(p).read().strip())


if __name__ == "__main__":
    main(sys.argv[1])

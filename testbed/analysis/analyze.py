#!/usr/bin/env python3
"""Analyze iotsim campaign runs (standard library only).

  analyze.py runs <campaign_dir> [-o summary.csv]
  analyze.py aggregate <summary.csv> [-o aggregate.csv]
  analyze.py cdf <summary.csv> --metric http_svc_recovery_s [-o cdf.csv]
"""
import argparse
import calendar
import csv
import json
import math
import os
import re
import sys

NS = 1_000_000_000
SETTLE_NS = 15 * NS  # ignore the tail so in-flight messages are not counted lost
STREAK = 10
GROUPS = ("q1", "q0")
MQTT_FIELDS = ("published", "delivered", "delivery", "lat_p50_ms", "lat_p99_ms",
               "lat_max_ms", "reconnects", "max_gap_s")
HTTP_FIELDS = ("failed", "recovery_s", "recovered", "failback_failed")
ID_COLS = ["config", "fault", "rep"]


def percentile(values, p):
    """Nearest-rank percentile (p in 0..100). Returns None for empty input."""
    if not values:
        return None
    s = sorted(values)
    rank = max(1, math.ceil(p / 100.0 * len(s)))
    return s[min(rank, len(s)) - 1]


# ---------------------------------------------------------------- parsing

def read_lines(path):
    """Yield stripped lines of a file; nothing if it is missing/unreadable."""
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                line = line.strip()
                if line:
                    yield line
    except OSError:
        return


def exists(path):
    return os.path.isfile(path)


def _int(s):
    try:
        return int(s)
    except (TypeError, ValueError):
        return None


def parse_http(path):
    """name -> sorted list of (ts, code)."""
    out = {}
    for line in read_lines(path):
        f = line.split()
        if len(f) < 5 or f[1] != "http":
            continue
        ts, code = _int(f[0]), _int(f[3])
        if ts is None or code is None:
            continue
        out.setdefault(f[2], []).append((ts, code))
    for v in out.values():
        v.sort()
    return out


def parse_pub(path):
    """Return (pubs{(client,seq):ts}, connlost_ts list)."""
    pubs, lost = {}, []
    for line in read_lines(path):
        f = line.split()
        if len(f) < 3:
            continue
        ts = _int(f[0])
        if ts is None:
            continue
        if f[1] == "pub" and len(f) >= 4:
            seq = _int(f[3])
            if seq is not None and (f[2], seq) not in pubs:
                pubs[(f[2], seq)] = ts
        elif f[1] == "connlost":
            lost.append(ts)
    return pubs, lost


def parse_sub(path):
    """Return list of (recv_ts, client, seq, sent_ns)."""
    recs = []
    for line in read_lines(path):
        f = line.split()
        if len(f) < 5 or f[1] != "recv":
            continue
        ts, seq, sent = _int(f[0]), _int(f[3]), _int(f[4])
        if None in (ts, seq, sent):
            continue
        recs.append((ts, f[2], seq, sent))
    return recs


_TS_RE = re.compile(r"^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)")
_TO_RE = re.compile(r'"to"\s*:\s*"([^"]*)"')


def parse_rfc3339_ns(s):
    m = _TS_RE.match(s)
    if not m:
        return None
    y, mo, d, h, mi, sec = (int(m.group(i)) for i in range(1, 7))
    secs = calendar.timegm((y, mo, d, h, mi, sec, 0, 0, 0))
    frac = (m.group(7) or "0")[:9].ljust(9, "0")
    tz = m.group(8)
    if tz != "Z":
        sign = 1 if tz[0] == "+" else -1
        secs -= sign * (int(tz[1:3]) * 3600 + int(tz[4:6]) * 60)
    return secs * NS + int(frac)


def parse_router(path):
    """List of (ts_ns, to_value) for 'Changed path' lines."""
    out = []
    for line in read_lines(path):
        if "Changed path" not in line:
            continue
        ts = parse_rfc3339_ns(line)
        if ts is None:
            continue
        m = _TO_RE.search(line)
        out.append((ts, m.group(1) if m else ""))
    out.sort(key=lambda x: x[0])
    return out


# ---------------------------------------------------------------- metrics

def http_metrics(samples, t_fault, t_clear, t_end):
    """samples: sorted list of (ts, code) for one probe name."""
    fault = [(t, c) for t, c in samples if t_fault <= t < t_clear]
    rec = [(t, c) for t, c in samples if t_clear <= t < t_end]
    m = {"failed": None, "recovery_s": None, "recovered": None,
         "failback_failed": None}
    if rec:
        m["failback_failed"] = sum(1 for _, c in rec if c != 200)
    if not fault:
        return m
    failed = sum(1 for _, c in fault if c != 200)
    m["failed"] = failed
    if failed == 0:
        m["recovery_s"] = 0.0
        m["recovered"] = 1
        return m
    first_fail = next(t for t, c in fault if c != 200)
    run_start, run_len = None, 0
    found = None
    for t, c in fault:
        if t < first_fail:
            continue
        if c == 200:
            if run_len == 0:
                run_start = t
            run_len += 1
            if run_len >= STREAK:
                found = run_start
                break
        else:
            run_len = 0
    if found is None:
        m["recovery_s"] = (t_clear - t_fault) / NS
        m["recovered"] = 0
    else:
        m["recovery_s"] = (found - t_fault) / NS
        m["recovered"] = 1
    return m


def mqtt_metrics(group, pub_path, sub_path, t_fault, t_clear, t_end):
    m = {k: None for k in MQTT_FIELDS}
    have_pub = exists(pub_path)
    have_sub = exists(sub_path)
    prefix = group + "-"
    pubs, lost = parse_pub(pub_path) if have_pub else ({}, [])
    recs = [r for r in parse_sub(sub_path) if r[1].startswith(prefix)] if have_sub else []

    if have_pub:
        m["reconnects"] = sum(1 for t in lost if t_fault <= t < t_end)
        cutoff = t_end - SETTLE_NS
        counted = {k: t for k, t in pubs.items() if t < cutoff}
        m["published"] = len(counted)
        if have_sub:
            first_recv = {}
            for ts, cid, seq, sent in recs:
                k = (cid, seq)
                if k not in first_recv or ts < first_recv[k][0]:
                    first_recv[k] = (ts, sent)
            delivered = [k for k in counted if k in first_recv]
            m["delivered"] = len(delivered)
            if counted:
                m["delivery"] = len(delivered) / len(counted)
            lats = [(first_recv[k][0] - first_recv[k][1]) / 1e6
                    for k in delivered if t_fault <= counted[k] < t_clear]
            if lats:
                m["lat_p50_ms"] = percentile(lats, 50)
                m["lat_p99_ms"] = percentile(lats, 99)
                m["lat_max_ms"] = max(lats)

    if have_sub:
        per_client = {}
        for ts, cid, _, _ in recs:
            if t_fault <= ts < t_end:
                per_client.setdefault(cid, []).append(ts)
        # clients known from the publisher log but silent in the window
        for cid, _ in pubs:
            per_client.setdefault(cid, [])
        if per_client:
            worst = 0
            for ts_list in per_client.values():
                ts_list.sort()
                prev = t_fault
                for t in ts_list:
                    worst = max(worst, t - prev)
                    prev = t
                if not ts_list:
                    worst = max(worst, t_end - t_fault)
            m["max_gap_s"] = worst / NS
    return m


def analyze_run(run_dir, meta):
    t_fault, t_clear, t_end = meta["t_fault_ns"], meta["t_clear_ns"], meta["t_end_ns"]
    row = {"config": meta.get("config", ""), "fault": meta.get("fault", ""),
           "rep": meta.get("rep", "")}
    for name, samples in parse_http(os.path.join(run_dir, "http.log")).items():
        for k, v in http_metrics(samples, t_fault, t_clear, t_end).items():
            row["http_%s_%s" % (name, k)] = v
    sub_path = os.path.join(run_dir, "sub.log")
    for g in GROUPS:
        mm = mqtt_metrics(g, os.path.join(run_dir, "pub-%s.log" % g), sub_path,
                          t_fault, t_clear, t_end)
        for k, v in mm.items():
            row["mqtt_%s_%s" % (g, k)] = v
    changes = parse_router(os.path.join(run_dir, "router-src.log"))
    steer = next(((t, to) for t, to in changes if t >= t_fault), None)
    if steer:
        row["steer_s"] = (steer[0] - t_fault) / NS
        row["steer_to"] = steer[1]
    # The path in use when the fault ends, e.g. "relay" after backup also failed.
    during = [(t, to) for t, to in changes if t_fault <= t < t_clear]
    if during:
        row["steer_final"] = during[-1][1]
        row["steer_final_s"] = (during[-1][0] - t_fault) / NS
    fb = next((t for t, to in changes if t >= t_clear and to == "primary"), None)
    if fb is not None:
        row["failback_s"] = (fb - t_clear) / NS
    return row


def find_runs(campaign):
    runs = []
    for root, dirs, files in os.walk(campaign):
        dirs.sort()
        if "meta.json" in files:
            runs.append(root)
    return sorted(runs)


def fmt(v):
    if v is None:
        return ""
    if isinstance(v, float):
        return repr(round(v, 6))
    return str(v)


def column_order(rows):
    names = set()
    for r in rows:
        for k in r:
            if k.startswith("http_"):
                for s in sorted(HTTP_FIELDS, key=len, reverse=True):
                    if k.endswith("_" + s):
                        names.add(k[5:-len(s) - 1])
                        break
    cols = list(ID_COLS)
    for n in sorted(names):
        cols += ["http_%s_%s" % (n, s) for s in HTTP_FIELDS]
    for g in GROUPS:
        cols += ["mqtt_%s_%s" % (g, s) for s in MQTT_FIELDS]
    cols += ["steer_s", "steer_to", "steer_final", "steer_final_s", "failback_s"]
    return cols


def cmd_runs(args):
    rows = []
    for rd in find_runs(args.campaign):
        try:
            with open(os.path.join(rd, "meta.json"), encoding="utf-8") as f:
                meta = json.load(f)
            rows.append(analyze_run(rd, meta))
        except (OSError, ValueError, KeyError, TypeError) as e:
            print("skipping %s: %s" % (rd, e), file=sys.stderr)
    rows.sort(key=lambda r: (str(r["config"]), str(r["fault"]), _int(r["rep"]) or 0))
    cols = column_order(rows)
    out = open(args.output, "w", newline="", encoding="utf-8") if args.output else sys.stdout
    try:
        w = csv.writer(out)
        w.writerow(cols)
        for r in rows:
            w.writerow([fmt(r.get(c)) for c in cols])
    finally:
        if args.output:
            out.close()
    return 0


def read_csv(path):
    with open(path, newline="", encoding="utf-8") as f:
        r = csv.DictReader(f)
        return r.fieldnames or [], list(r)


def _float(s):
    try:
        return float(s)
    except (TypeError, ValueError):
        return None


def cmd_aggregate(args):
    header, rows = read_csv(args.summary)
    metrics = [c for c in header if c not in ID_COLS and
               any(_float(r.get(c)) is not None for r in rows)]
    groups = {}
    for r in rows:
        groups.setdefault((r["config"], r["fault"]), []).append(r)
    cols = ["config", "fault", "n"]
    for m in metrics:
        cols += [m + "_median", m + "_p95", m + "_mean"]
    out = open(args.output, "w", newline="", encoding="utf-8") if args.output else sys.stdout
    try:
        w = csv.writer(out)
        w.writerow(cols)
        for (cfg, fault), rs in sorted(groups.items()):
            row = [cfg, fault, len(rs)]
            for m in metrics:
                vals = [v for v in (_float(r.get(m)) for r in rs) if v is not None]
                if vals:
                    row += [fmt(percentile(vals, 50)), fmt(percentile(vals, 95)),
                            fmt(sum(vals) / len(vals))]
                else:
                    row += ["", "", ""]
            w.writerow(row)
    finally:
        if args.output:
            out.close()
    return 0


def cmd_cdf(args):
    header, rows = read_csv(args.summary)
    if args.metric not in header:
        print("metric %s not in %s" % (args.metric, args.summary), file=sys.stderr)
        return 1
    groups = {}
    for r in rows:
        v = _float(r.get(args.metric))
        if v is not None:
            groups.setdefault((r["config"], r["fault"]), []).append(v)
    out = open(args.output, "w", newline="", encoding="utf-8") if args.output else sys.stdout
    try:
        w = csv.writer(out)
        w.writerow(["config", "fault", "value", "cum_fraction"])
        for (cfg, fault), vals in sorted(groups.items()):
            vals.sort()
            for i, v in enumerate(vals, 1):
                w.writerow([cfg, fault, fmt(v), fmt(i / len(vals))])
    finally:
        if args.output:
            out.close()
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sp = ap.add_subparsers(dest="cmd", required=True)
    p = sp.add_parser("runs")
    p.add_argument("campaign")
    p.add_argument("-o", "--output")
    p.set_defaults(fn=cmd_runs)
    p = sp.add_parser("aggregate")
    p.add_argument("summary")
    p.add_argument("-o", "--output")
    p.set_defaults(fn=cmd_aggregate)
    p = sp.add_parser("cdf")
    p.add_argument("summary")
    p.add_argument("--metric", required=True)
    p.add_argument("-o", "--output")
    p.set_defaults(fn=cmd_cdf)
    args = ap.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())

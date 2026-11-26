import csv
import io
import json
import os
import shutil
import sys
import tempfile
import unittest
from contextlib import redirect_stdout

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import analyze  # noqa: E402

S = 1_000_000_000
T0 = 1_700_000_000 * S          # t_start
TF = T0 + 10 * S                # fault
TC = TF + 20 * S                # clear
TE = TC + 30 * S                # end


def iso(ns):
    import datetime
    dt = datetime.datetime.fromtimestamp(ns // S, datetime.timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S") + ".%09dZ" % (ns % S)


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def make_run(root, config, fault, rep, **files):
    d = os.path.join(root, config, fault, "rep%02d" % rep)
    meta = {"config": config, "fault": fault, "rep": rep, "t_start_ns": T0,
            "t_fault_ns": TF, "t_clear_ns": TC, "t_end_ns": TE,
            "src_node": "a", "dst_node": "b"}
    write(os.path.join(d, "meta.json"), json.dumps(meta))
    for name, text in files.items():
        write(os.path.join(d, name.replace("_", "-", 1) if name.startswith(("pub_", "router_")) else name), text)
    return d


def http_lines(name, outage_from, outage_to, start=T0, end=TE, step=S // 10):
    out = []
    t = start
    while t < end:
        bad = outage_from is not None and outage_from <= t < outage_to
        out.append("%d http %s %d %d" % (t, name, 0 if bad else 200, 500000 if bad else 800))
        t += step
    return "\n".join(out) + "\n"


class AnalyzeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

    def rows(self):
        out = os.path.join(self.tmp, "summary.csv")
        analyze.main(["runs", os.path.join(self.tmp, "camp"), "-o", out])
        with open(out, newline="") as f:
            return {(r["config"], r["fault"], int(r["rep"])): r for r in csv.DictReader(f)}

    def test_percentile(self):
        self.assertIsNone(analyze.percentile([], 50))
        self.assertEqual(analyze.percentile([1, 2, 3, 4], 50), 2)
        self.assertEqual(analyze.percentile([1, 2, 3, 4], 95), 4)
        self.assertEqual(analyze.percentile(list(range(1, 101)), 99), 99)

    def test_http_outage_and_router(self):
        camp = os.path.join(self.tmp, "camp")
        outage = (TF + 1 * S, TF + 4 * S)
        router = ("%s some prefix Changed path {\"peer\": \"m03\", \"from\": \"primary\", \"to\": \"backup\", \"via\": \"\"}\n"
                  "%s other line\n"
                  "%s Changed path {\"peer\": \"m03\", \"from\": \"backup\", \"to\": \"primary\", \"via\": \"\"}\n"
                  % (iso(TF + 2 * S + 500), iso(TF + 3 * S), iso(TC + 5 * S)))
        make_run(camp, "cni", "linkdown", 1,
                 **{"http.log": http_lines("svc", *outage) + http_lines("pod", None, None),
                    "router_src.log": router})
        r = self.rows()[("cni", "linkdown", 1)]
        self.assertEqual(r["http_svc_failed"], "30")
        # first success of the streak: the outage ends at TF+4s
        self.assertEqual(float(r["http_svc_recovery_s"]), 4.0)
        self.assertEqual(r["http_svc_recovered"], "1")
        self.assertEqual(r["http_svc_failback_failed"], "0")
        self.assertEqual(r["http_pod_failed"], "0")
        self.assertEqual(float(r["http_pod_recovery_s"]), 0.0)
        self.assertAlmostEqual(float(r["steer_s"]), 2.0000005, places=5)
        self.assertEqual(r["steer_to"], "backup")
        self.assertEqual(float(r["failback_s"]), 5.0)

    def test_never_recovers_and_failback_failures(self):
        camp = os.path.join(self.tmp, "camp")
        log = http_lines("svc", TF + S, TE)
        make_run(camp, "base", "down", 1, **{"http.log": log})
        r = self.rows()[("base", "down", 1)]
        self.assertEqual(r["http_svc_recovered"], "0")
        self.assertEqual(float(r["http_svc_recovery_s"]), 20.0)
        self.assertEqual(r["http_svc_failback_failed"], "300")
        self.assertEqual(r["steer_s"], "")

    def test_no_outage(self):
        camp = os.path.join(self.tmp, "camp")
        make_run(camp, "cni", "none", 1, **{"http.log": http_lines("svc", None, None)})
        r = self.rows()[("cni", "none", 1)]
        self.assertEqual(r["http_svc_failed"], "0")
        self.assertEqual(float(r["http_svc_recovery_s"]), 0.0)
        self.assertEqual(r["http_svc_recovered"], "1")

    def test_missing_files_do_not_crash(self):
        camp = os.path.join(self.tmp, "camp")
        make_run(camp, "base", "x", 1, **{"http.log": http_lines("svc", None, None)})
        make_run(camp, "base", "y", 1)
        rows = self.rows()
        r = rows[("base", "x", 1)]
        for k in ("mqtt_q1_published", "mqtt_q1_delivery", "mqtt_q0_max_gap_s",
                  "steer_s", "steer_to", "failback_s"):
            self.assertEqual(r[k], "", k)
        self.assertEqual(rows[("base", "y", 1)]["steer_s"], "")

    def test_q1_reconnect_delayed_delivery(self):
        camp = os.path.join(self.tmp, "camp")
        pub, sub = [], []
        # one client, 1 msg/s from T0; messages 1..60 (published T0+1..T0+60 s)
        cid = "q1-s00"
        for seq in range(1, 61):
            ts = T0 + seq * S
            pub.append("%d pub %s %d" % (ts, cid, seq))
            pub.append("%d ack %s %d ok" % (ts + 1000, cid, seq))
            # messages sent during the fault (TF+1 .. TF+5) arrive 8 s late
            late = 8 * S if TF < ts <= TF + 5 * S else 5_000_000
            sub.append("%d recv %s %d %d 1" % (ts + late, cid, seq, ts))
        pub.append("%d connlost %s EOF" % (TF + 2 * S, cid))
        pub.append("%d connected %s" % (TF + 9 * S, cid))
        make_run(camp, "cni", "partition", 1,
                 **{"pub_q1.log": "\n".join(pub) + "\n", "sub.log": "\n".join(sub) + "\n"})
        r = self.rows()[("cni", "partition", 1)]
        # TE = T0+60 s; cutoff = T0+45 s => seqs 1..44 counted
        self.assertEqual(r["mqtt_q1_published"], "44")
        self.assertEqual(r["mqtt_q1_delivered"], "44")
        self.assertEqual(float(r["mqtt_q1_delivery"]), 1.0)
        self.assertEqual(r["mqtt_q1_reconnects"], "1")
        # fault-window messages: seq 11..30; 11..15 late by 8 s, rest 5 ms
        self.assertEqual(float(r["mqtt_q1_lat_max_ms"]), 8000.0)
        self.assertEqual(float(r["mqtt_q1_lat_p50_ms"]), 5.0)
        # no recv between TF and the first late arrival => a multi-second gap
        self.assertGreater(float(r["mqtt_q1_max_gap_s"]), 5.0)
        self.assertEqual(r["mqtt_q0_published"], "")

    def test_q0_lost_messages(self):
        camp = os.path.join(self.tmp, "camp")
        pub, sub = [], []
        for cid in ("q0-s00", "q0-s01"):
            for seq in range(1, 51):
                ts = T0 + seq * S
                pub.append("%d pub %s %d" % (ts, cid, seq))
                lost = TF + 2 * S <= ts < TF + 12 * S  # 10 lost per client
                if not lost:
                    sub.append("%d recv %s %d %d 0" % (ts + 2_000_000, cid, seq, ts))
        # malformed/unrelated lines must be ignored
        sub.append("garbage")
        sub.append("%d connected sub" % T0)
        make_run(camp, "cni", "loss", 1,
                 **{"pub_q0.log": "\n".join(pub) + "\n", "sub.log": "\n".join(sub) + "\n"})
        r = self.rows()[("cni", "loss", 1)]
        self.assertEqual(r["mqtt_q0_published"], "88")  # 2 clients * 44
        self.assertEqual(r["mqtt_q0_delivered"], "68")
        self.assertAlmostEqual(float(r["mqtt_q0_delivery"]), 68 / 88, places=5)
        self.assertEqual(r["mqtt_q0_reconnects"], "0")
        self.assertGreaterEqual(float(r["mqtt_q0_max_gap_s"]), 10.0)

    def test_aggregate_and_cdf(self):
        camp = os.path.join(self.tmp, "camp")
        for rep, end in ((1, TF + 2 * S), (2, TF + 3 * S), (3, TF + 4 * S)):
            make_run(camp, "cni", "linkdown", rep,
                     **{"http.log": http_lines("svc", TF, end)})
        summary = os.path.join(self.tmp, "s.csv")
        agg = os.path.join(self.tmp, "a.csv")
        cdf = os.path.join(self.tmp, "c.csv")
        analyze.main(["runs", camp, "-o", summary])
        analyze.main(["aggregate", summary, "-o", agg])
        analyze.main(["cdf", summary, "--metric", "http_svc_recovery_s", "-o", cdf])
        with open(agg, newline="") as f:
            rows = list(csv.DictReader(f))
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n"], "3")
        self.assertEqual(float(rows[0]["http_svc_recovery_s_median"]), 3.0)
        self.assertEqual(float(rows[0]["http_svc_recovery_s_p95"]), 4.0)
        self.assertEqual(float(rows[0]["http_svc_recovery_s_mean"]), 3.0)
        with open(cdf, newline="") as f:
            c = list(csv.DictReader(f))
        self.assertEqual([float(x["value"]) for x in c], [2.0, 3.0, 4.0])
        self.assertAlmostEqual(float(c[-1]["cum_fraction"]), 1.0)

    def test_merge_replaces_whole_cells(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        base, over, out = (os.path.join(d, n) for n in ("b.csv", "o.csv", "m.csv"))
        with open(base, "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["config", "fault", "rep", "x"])
            w.writerows([["full", "cut", 1, 1], ["full", "cut", 2, 2], ["full", "loss", 1, 3]])
        with open(over, "w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["config", "fault", "rep", "x", "y"])
            w.writerow(["full", "cut", 1, 9, 7])
        analyze.main(["merge", base, over, "-o", out])
        with open(out, newline="") as f:
            rows = list(csv.DictReader(f))
        self.assertEqual([(r["fault"], r["rep"], r["x"], r["y"]) for r in rows],
                         [("cut", "1", "9", "7"), ("loss", "1", "3", "")])

    def test_rfc3339_parse(self):
        self.assertEqual(analyze.parse_rfc3339_ns("2026-11-25T10:26:23.123456789Z x"),
                         analyze.parse_rfc3339_ns("2026-11-25T10:26:23Z") + 123456789)


if __name__ == "__main__":
    unittest.main()

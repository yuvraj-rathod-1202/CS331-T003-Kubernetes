/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command ipam-sim compares pod IP allocation policies on synthetic workloads
// and writes tables and charts. See internal/controller/ipam/sim.
//
//nolint:lll // format strings
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"CS331-CN-Project-1/operator/internal/controller/ipam/sim"
)

func main() {
	out := flag.String("out", "docs/ipam-eval", "Output directory.")
	seeds := flag.Int("seeds", 3, "Seeds per scenario and policy.")
	sweepMax := flag.Int("sweep-max", 1000, "Largest node count in the sweep.")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}

	var rows []sim.Result
	byScn := map[string]map[string][]sim.Result{}
	record := func(r sim.Result) {
		rows = append(rows, r)
		if byScn[r.Scenario] == nil {
			byScn[r.Scenario] = map[string][]sim.Result{}
		}
		byScn[r.Scenario][r.Policy] = append(byScn[r.Scenario][r.Policy], r)
	}

	policies := append(append([]sim.PolicyName{}, sim.AllPolicies...), sim.BuddyNoStarve, sim.BuddyNoShrink)
	for _, s := range sim.Scenarios() {
		for _, p := range policies {
			for seed := int64(1); seed <= int64(*seeds); seed++ {
				record(sim.Run(s, p, seed))
			}
		}
		fmt.Fprintln(os.Stderr, "done", s.Name)
	}

	writeSummary(*out, rows)
	writeTable(*out, byScn)
	writeSeries(*out, byScn["burst"])
	writeCharts(*out, byScn)
	writeSweep(*out, *sweepMax)
	fmt.Println("wrote", *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func mean(rs []sim.Result, f func(sim.Result) float64) float64 {
	t := 0.0
	for _, r := range rs {
		t += f(r)
	}
	return t / float64(len(rs))
}

func writeSummary(dir string, rows []sim.Result) {
	var sb strings.Builder
	sb.WriteString("scenario,policy,seed,utilization_pct,addrs_per_node,unmet_pod_seconds,peak_unmet_pods,pods_served_pct,peak_pool_pct,peak_routes,block_ops,nodes_with_space\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s,%s,%d,%.2f,%.1f,%.0f,%d,%.2f,%.2f,%d,%d,%d\n", r.Scenario, r.Policy, r.Seed,
			r.Utilization, r.AllocPerNode, r.UnmetPodSecs, r.PeakUnmet, r.PodsServedPct, r.PoolPercent, r.PeakRoutes, r.Ops, r.Admitted)
	}
	must(os.WriteFile(filepath.Join(dir, "summary.csv"), []byte(sb.String()), 0o644))
}

const policyBuddy = "buddy"

var mainNames = []string{policyBuddy, "static-/24", "static-/25", "calico-64"}

var order = []string{"steady", "skewed", "burst", "wave", "tight-pool"}

func writeTable(dir string, by map[string]map[string][]sim.Result) {
	var sb strings.Builder
	sb.WriteString("| scenario | policy | pods served % | unmet pod-min | peak unmet | utilisation % | addrs/node | peak routes | block ops |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, sc := range order {
		for _, p := range append(append([]string{}, mainNames...), "buddy-no-emergency", "buddy-no-shrink") {
			rs := by[sc][p]
			if len(rs) == 0 {
				continue
			}
			fmt.Fprintf(&sb, "| %s | %s | %.1f | %.0f | %.0f | %.1f | %.0f | %.0f | %.0f |\n", sc, p,
				mean(rs, func(r sim.Result) float64 { return r.PodsServedPct }),
				mean(rs, func(r sim.Result) float64 { return r.UnmetPodSecs / 60 }),
				mean(rs, func(r sim.Result) float64 { return float64(r.PeakUnmet) }),
				mean(rs, func(r sim.Result) float64 { return r.Utilization }),
				mean(rs, func(r sim.Result) float64 { return r.AllocPerNode }),
				mean(rs, func(r sim.Result) float64 { return float64(r.PeakRoutes) }),
				mean(rs, func(r sim.Result) float64 { return float64(r.Ops) }))
		}
	}
	must(os.WriteFile(filepath.Join(dir, "results.md"), []byte(sb.String()), 0o644))
}

func writeSeries(dir string, by map[string][]sim.Result) {
	var sb strings.Builder
	sb.WriteString("policy,minute,pods,wanted,allocated,unmet,routes\n")
	for _, p := range mainNames {
		if len(by[p]) == 0 {
			continue
		}
		for _, pt := range by[p][0].Series {
			fmt.Fprintf(&sb, "%s,%.2f,%d,%d,%d,%d,%d\n", p, pt.Minutes, pt.Pods, pt.Wanted, pt.Allocated, pt.Unmet, pt.Routes)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "series-burst.csv"), []byte(sb.String()), 0o644))
}

func writeCharts(dir string, by map[string]map[string][]sim.Result) {
	names := mainNames

	// Addresses held per node over the burst scenario, with the pods wanted.
	lines := make([]sim.Result, 0, len(names))
	for _, p := range names {
		lines = append(lines, by["burst"][p][0])
	}
	nodes := 50.0
	ls := make([]line, 0, len(lines)+1)
	for i, r := range lines {
		l := line{Name: names[i]}
		for _, pt := range r.Series {
			l.X = append(l.X, pt.Minutes)
			l.Y = append(l.Y, float64(pt.Allocated)/nodes)
		}
		ls = append(ls, l)
	}
	want := line{Name: "pods wanted"}
	for _, pt := range lines[0].Series {
		want.X = append(want.X, pt.Minutes)
		want.Y = append(want.Y, float64(pt.Wanted)/nodes)
	}
	ls = append(ls, want)
	must(os.WriteFile(filepath.Join(dir, "burst-addresses.svg"), []byte(lineSVG(
		chart{Title: "Addresses held per node during a burst (50 nodes)", XLabel: "minutes", YLabel: "addresses per node", W: 760, H: 380},
		ls, []float64{0, 10, 20, 30, 40, 50, 60}, fmtNum)), 0o644))

	// Share of wanted pods that got an address, per scenario.
	for _, m := range []struct {
		file, title, y string
		f              func(sim.Result) float64
		hint           float64
	}{
		{"pods-served.svg", "Share of wanted pods that got an address", "% of pods served", func(r sim.Result) float64 { return r.PodsServedPct }, 100},
		{"utilization.svg", "Pods per allocated address", "utilisation %", func(r sim.Result) float64 { return r.Utilization }, 100},
	} {
		vals := make([][]float64, len(names))
		for i, p := range names {
			for _, sc := range order {
				vals[i] = append(vals[i], mean(by[sc][p], m.f))
			}
		}
		must(os.WriteFile(filepath.Join(dir, m.file), []byte(barSVG(
			chart{Title: m.title, YLabel: m.y, W: 760, H: 380}, order, names, vals, m.hint)), 0o644))
	}
}

func writeSweep(dir string, maxNodes int) {
	counts := []int{25, 50, 100, 200, 400, 600, 800, 1000}
	names := mainNames
	var sb strings.Builder
	sb.WriteString("nodes,policy,nodes_with_space,pods_served_pct,pool_pct,peak_routes\n")
	served := make([]line, len(names))
	for i, n := range names {
		served[i].Name = n
	}
	for _, c := range counts {
		if c > maxNodes {
			continue
		}
		for i, p := range sim.AllPolicies {
			r := sim.Run(sim.SweepScenario(c), p, 1)
			fmt.Fprintf(&sb, "%d,%s,%d,%.1f,%.1f,%d\n", c, p, r.Admitted, r.PodsServedPct, r.PoolPercent, r.PeakRoutes)
			served[i].X = append(served[i].X, float64(c))
			served[i].Y = append(served[i].Y, r.PodsServedPct)
		}
		fmt.Fprintln(os.Stderr, "sweep", c)
	}
	must(os.WriteFile(filepath.Join(dir, "sweep.csv"), []byte(sb.String()), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sweep.svg"), []byte(lineSVG(
		chart{Title: "Pods served as the cluster grows (60 pods per node, /16 pool)", XLabel: "nodes", YLabel: "% of pods served", W: 760, H: 380},
		served, []float64{0, 200, 400, 600, 800, 1000}, fmtNum)), 0o644))
}

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

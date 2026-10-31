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

//nolint:lll // format strings and SVG templates
package main

import (
	"fmt"
	"math"
	"strings"
)

// Series colours follow the dataviz reference palette (categorical slots 1, 2,
// 3 and 5, light / dark). Lines also differ in dash pattern, so colour is never
// the only cue.
var (
	lightColors = []string{"#2a78d6", "#eb6834", "#1baf7a", "#e87ba4", "#6250d6", "#eda100"}
	darkColors  = []string{"#3987e5", "#d95926", "#199e70", "#d55181", "#9085e9", "#c98500"}
	dashes      = []string{"", "6 3", "2 3", "8 3 2 3", "", "6 3"}
)

type line struct {
	Name string
	X, Y []float64
}

type chart struct {
	Title, XLabel, YLabel string
	W, H                  int
}

const svgHead = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" role="img" aria-label="%s">
<style>
svg{--bg:#fcfcfb;--ink:#0b0b0b;--ink2:#52514e;--grid:#e7e6e2;%s}
@media (prefers-color-scheme:dark){svg{--bg:#1a1a19;--ink:#f0efec;--ink2:#c3c2b7;--grid:#383835;%s}}
text{font:12px system-ui,sans-serif;fill:var(--ink2)} .t{font-size:14px;font-weight:600;fill:var(--ink)}
.g{stroke:var(--grid);stroke-width:1} .a{stroke:var(--ink2);stroke-width:1}
</style>
<rect width="100%%" height="100%%" fill="var(--bg)"/>
`

func colorVars(light bool) string {
	cs := darkColors
	if light {
		cs = lightColors
	}
	var b strings.Builder
	for i, c := range cs {
		fmt.Fprintf(&b, "--c%d:%s;", i, c)
	}
	return b.String()
}

func head(c chart) string {
	return fmt.Sprintf(svgHead, c.W, c.H, c.W, esc(c.Title), colorVars(true), colorVars(false))
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// niceMax rounds up to a 1/2/5 step so the axis ends on a tick.
func niceMax(v float64) (max, step float64) {
	if v <= 0 {
		return 1, 0.2
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if v <= m*exp {
			return m * exp, m * exp / 5
		}
	}
	return 10 * exp, 2 * exp
}

type frame struct {
	l, r, t, b float64
	ymax       float64
	xmin, xmax float64
}

func (f frame) px(x float64) float64 { return f.l + (x-f.xmin)/(f.xmax-f.xmin)*(f.r-f.l) }
func (f frame) py(y float64) float64 { return f.b - y/f.ymax*(f.b-f.t) }

func axes(sb *strings.Builder, c chart, f frame, step float64, xticks []float64, xfmt func(float64) string) {
	fmt.Fprintf(sb, `<text class="t" x="%g" y="22">%s</text>`+"\n", f.l, esc(c.Title))
	for v := 0.0; v <= f.ymax+step/2; v += step {
		y := f.py(v)
		fmt.Fprintf(sb, `<line class="g" x1="%g" x2="%g" y1="%.1f" y2="%.1f"/><text x="%g" y="%.1f" text-anchor="end">%s</text>`+"\n",
			f.l, f.r, y, y, f.l-6, y+4, fmtNum(v))
	}
	fmt.Fprintf(sb, `<line class="a" x1="%g" x2="%g" y1="%g" y2="%g"/>`+"\n", f.l, f.r, f.b, f.b)
	for _, x := range xticks {
		fmt.Fprintf(sb, `<text x="%.1f" y="%g" text-anchor="middle">%s</text>`+"\n", f.px(x), f.b+16, xfmt(x))
	}
	fmt.Fprintf(sb, `<text x="%g" y="%d" text-anchor="middle">%s</text>`+"\n", (f.l+f.r)/2, c.H-8, esc(c.XLabel))
	fmt.Fprintf(sb, `<text transform="translate(14 %g) rotate(-90)" text-anchor="middle">%s</text>`+"\n", (f.t+f.b)/2, esc(c.YLabel))
}

func fmtNum(v float64) string {
	switch {
	case v >= 1000 && math.Mod(v, 1000) == 0:
		return fmt.Sprintf("%gk", v/1000)
	case v == math.Trunc(v):
		return fmt.Sprintf("%g", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func legend(sb *strings.Builder, names []string, x, y float64) {
	for i, n := range names {
		fmt.Fprintf(sb, `<line x1="%g" x2="%g" y1="%g" y2="%g" stroke="var(--c%d)" stroke-width="2.5" stroke-dasharray="%s"/><text x="%g" y="%g">%s</text>`+"\n",
			x, x+22, y, y, i, dashes[i], x+27, y+4, esc(n))
		x += 40 + float64(len(n))*6.6
	}
}

// lineSVG draws lines with a shared x axis. names index colours in order.
func lineSVG(c chart, lines []line, xticks []float64, xfmt func(float64) string) string {
	var sb strings.Builder
	sb.WriteString(head(c))
	f := frame{l: 60, r: float64(c.W) - 20, t: 56, b: float64(c.H) - 40}
	var ymax float64
	f.xmin, f.xmax = math.Inf(1), math.Inf(-1)
	for _, ln := range lines {
		for i := range ln.X {
			ymax = math.Max(ymax, ln.Y[i])
			f.xmin, f.xmax = math.Min(f.xmin, ln.X[i]), math.Max(f.xmax, ln.X[i])
		}
	}
	var step float64
	f.ymax, step = niceMax(ymax)
	axes(&sb, c, f, step, xticks, xfmt)
	names := make([]string, len(lines))
	for i, ln := range lines {
		names[i] = ln.Name
		var pts []string
		for j := range ln.X {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", f.px(ln.X[j]), f.py(ln.Y[j])))
		}
		fmt.Fprintf(&sb, `<polyline fill="none" stroke="var(--c%d)" stroke-width="2" stroke-dasharray="%s" stroke-linejoin="round" points="%s"><title>%s</title></polyline>`+"\n",
			i, dashes[i], strings.Join(pts, " "), esc(ln.Name))
	}
	legend(&sb, names, f.l, 40)
	sb.WriteString("</svg>\n")
	return sb.String()
}

// barSVG draws grouped bars: one group per category, one bar per series.
func barSVG(c chart, cats, series []string, vals [][]float64, ymaxHint float64) string {
	var sb strings.Builder
	sb.WriteString(head(c))
	f := frame{l: 60, r: float64(c.W) - 20, t: 56, b: float64(c.H) - 40, xmin: 0, xmax: float64(len(cats))}
	var step float64
	f.ymax, step = niceMax(ymaxHint)
	axes(&sb, c, f, step, nil, nil)
	gw := (f.r - f.l) / float64(len(cats))
	bw := (gw - 16) / float64(len(series))
	for ci, cat := range cats {
		x0 := f.l + float64(ci)*gw + 8
		fmt.Fprintf(&sb, `<text x="%.1f" y="%g" text-anchor="middle">%s</text>`+"\n", x0+(gw-16)/2, f.b+16, esc(cat))
		for si := range series {
			v := vals[si][ci]
			h := v / f.ymax * (f.b - f.t)
			fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="3" fill="var(--c%d)"><title>%s, %s: %.1f</title></rect>`+"\n",
				x0+float64(si)*bw+1, f.b-h, bw-2, h, si, esc(series[si]), esc(cat), v)
		}
	}
	legend(&sb, series, f.l, 40)
	sb.WriteString("</svg>\n")
	return sb.String()
}

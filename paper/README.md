# 6DCIoT 2027 paper

Target: 6DCIoT workshop at IEEE PerCom 2027 (Goa, 8-12 March 2027).
Submission: https://6dciot27.hotcrp.com/ by **30 Nov 2026 AoE** (about 17:30 IST on 1 Dec).

## Build

```bash
python3 ../testbed/analysis/analyze.py runs <campaign> -o summary.csv
python3 ../testbed/analysis/plot.py summary.csv figs
latexmk -pdf main.tex
```

Red `[...]` marks numbers still to take from the campaign; blue `[TODO ...]` marks
text still to write. Missing figures show as labelled boxes.

## Before submitting

- [ ] 6 pages, plus at most 1 extra page, IEEE Computer Society format (`compsoc`)
- [ ] No red or blue markers left
- [ ] Double-blind: no names, course name or institute; no GitHub username anywhere
      (the CRD API group `remediation.cn-operator.yuvraj-rathod-1202.github.io` must not
      appear in the paper or in figures); link only an anonymised copy of the code
      (anonymous.4open.science); PDF metadata has no author name
- [ ] Every reference checked against the publisher's page
- [ ] Ask the chairs whether the arXiv/preprint policy matters for your plans

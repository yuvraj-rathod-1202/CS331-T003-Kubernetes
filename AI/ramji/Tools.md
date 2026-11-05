# Tools (after midsem)

**Primary Platform / Tool Used:** Claude Code (Anthropic's command-line coding agent), running in my terminal.

---

## 1. AI Models Used
Both models were used in the same chat (29 Oct to 5 Nov 2026, about 120 prompts). I switched from the first to the second with `/model` towards the end.
- **Claude Opus 5.5:** most of the design and debugging work on the IPAM operator: the buddy allocator, the planner (grow, shrink, drain, quarantine, release), the node agent, and the first versions of the demo page.
- **Claude Sonnet 5.5:** the later stretch: the growth-sizing and starting-block rules, splitting empty halves, the one-screen page and terminal-style log, the simulator re-runs, and the commit / pull request.

---

## 2. Skills & Plugins
- **`dataviz` (a Claude Code skill):** loaded once, for the evaluation charts produced by the simulator.
- No other skills or plugins were used for this part. (Graphify and Ponytail, listed in my earlier notes, were not used here.)

---

## 3. Other tools the AI ran for me
Claude Code ran these through its shell tool, in my workspace:
- `kubectl` and `minikube` on a 3-node cluster (profile `buddy-ipam`) for every live check.
- The Go toolchain: `go test` (also with `-race`), `controller-gen` for the CRDs, and `golangci-lint`.
- Node, Vite and React for the demo website.
- A headless Firefox driven over WebDriver BiDi, to click the page and take screenshots.
- `git` and `gh` for the branches and the pull requests.

---

## 4. Access & Environment
Running locally on Linux in `~/iitgn/CS331-CN-Project-1`, directly on the repository and the local minikube cluster. I started and stopped the controller and the website myself in my own terminals; the AI never ran `sudo` and was told not to touch my other clusters.

---

## 5. Where AI Used It
For my part of the project, **Problem 1: the buddy-style dynamic pod IP allocation operator and its demo**:
- The IPAM controller and node agent: buddy allocation, growing and shrinking a node's blocks, draining, quarantine, leak reclaim, and the emergency grow from kubelet "no IP" events.
- Unit tests and lint fixes.
- The simulator `operator/cmd/ipam-sim`, which runs the real planner against fixed /24, fixed /25 and Calico-style baselines.
- The documentation: the controller and agent docs, the decision log, the evaluation, and `DEMO_GUIDE.md`.
- The live demo page `/project1` and the small dev-server endpoints behind it.
- Debugging on the live cluster, from what I saw on the page.
- Git work: rebasing onto `main`, and the pull requests (#23 and #28).

---

## 6. Conversation files in this folder
- `claude_code_ipam_demo_session.jsonl`: the full chat for this work (33 MB).
- `claude_code_short_session_login_error.jsonl`: a very short earlier chat (24 Oct); it contains only one request and a Claude Code login error, with no work in it.

The original files are in `~/.claude/projects/-home-ramji-iitgn-CS331-CN-Project-1/`.

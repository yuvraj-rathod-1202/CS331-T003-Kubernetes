After midsem I worked on **Problem 1: the buddy-style dynamic pod IP allocation operator** (the controller that hands out address blocks to nodes, and the agent on each node), and on its live demo page.

Here is how I used the AI and what I decided myself.

## What I asked for, in order
1. Finish the whole IPAM part: code, tests and a written list of limitations, then open the pull request.
2. Put the operator on the demo website. I insisted that the demo run on the **real cluster, not a simulation**, like the Problem 2 page. So the page reads the cluster with `kubectl`, and its buttons create real pods. The offline simulation stays on a separate page.
3. Rebase onto `main` so that both problems can be shown from the same site, with only the Problem 1 controller running.

## What I wanted the demo to be
- **Short:** one to two minutes, built on a real scenario ("fill the IP pool"), not artificial leases.
- **Everything on the page:** a small "Inject a fault" panel, the IP addresses in the middle (which node uses which range, each node in its own colour), and a log of what happens, written in plain English, in a terminal style.
- **Simple look:** white and muted colours, no neon. Later I asked for the buttons in a bar along the top, with the steps shown only after a button is pressed, and a box on each node to set its number of pods.
- **No hard-coded numbers:** the operator's decisions must come from the code and the pool's settings.

## What I found and corrected by watching the demo
Most of the later changes came from me running the page and noticing something wrong:
- A node was given a big block first and then half of it was taken back. I asked that it get **only what its pods need, in the first place**. This led to growth sized to the shortfall instead of a percentage of the node's size.
- I decided that a node's **starting block is never touched**, and that only blocks given to it later can be split or taken back.
- I asked that a block that is not fully needed be divided (**buddy halves**), and that merged blocks be shown as one block on the page.
- I reported cases where the controller grew, drained and released the same block over and over, and a block being drained while it was full of running pods. Each of these was reproduced on the cluster and fixed.
- I asked about the spare-room ("buffer") rule until I understood it: a node grows below the larger of 8 free addresses or 20% of its capacity. I accepted it, and asked for the demo pool to give space back sooner (shrink threshold 70%), on the cluster only.

## How I checked the AI's work
- Every change was tried on the 3-node cluster and judged from the page, the block objects and the events, not only from the tests.
- The tests and the linter had to pass, and the simulator was re-run after each rule change, so that the cost of a rule (for example more routes) was written down with the benefit.
- The docs and the decision log record each rule and why it changed.

The conversation is in `claude_code_ipam_demo_session.jsonl` in this folder. It shows the prompts I used and the step-by-step work.

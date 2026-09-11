# Thought Process — how I integrated AI into my workflow

I am being honest here about what the AI did and what I did, because that is the point of this disclosure.

## Starting point
Our team is building an operator for "Extending Kubernetes self-healing for network failures." My assigned module is **Pod-Connectivity**, which focuses on detecting and mitigating scenarios where node and pod statuses report as "Ready", but actual network connectivity is broken.

## How I used the AI
I used Antigravity IDE (with Gemini/Claude models) as an interactive pair programmer that could execute commands in my environment and help me reason about Kubernetes networking. 

- **Debugging the Probe Logic:** The AI helped me uncover a bug where my Python ping probe script was silently returning `0` even on failure because a catch-all exception block swallowed a `SystemExit`. I had the AI fix the probe commands to properly test TCP ports and handle fallback logic.
- **Node Triangulation & Remediation:** The AI helped me refine the operator's logic for determining node failure (empty ring blindness) and implementing correct node recovery (un-tainting and un-cordoning nodes successfully after recovery).

## What I decided / did vs. what the AI did
- **AI did:** Analyzed logs, spotted bugs in the Python probe strings, and tweaked Golang controller logic (`remediator.go`, `controller.go`, `triangulation.go`).
- **I did:** Drove the overall testing strategy, executed all the physical cluster commands on my WSL/Minikube setup, and verified the functionality.

## Why I used it
To rapidly debug subtle edge cases in a complex Go operator. The AI acted as a rapid research tool and a second pair of eyes on my logs, saving hours of manual debugging.

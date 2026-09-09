# Thought Process — how I integrated AI into my workflow

I am being honest here about what the AI did and what I did, because that is the
point of this disclosure.

## Starting point
Kubernetes was completely new to me. We chose this project mainly to learn, and
my teammate Yuvraj set up the operator skeleton where each of us owns one module.
I was assigned the **NetworkPolicy** module. My first problem was simply
understanding the codebase and the operator/CNI/DNS/NetworkPolicy concepts.

## How I used the AI
I used Claude Code as a "pair" that could read the real repo and explain it, then
help me write my module in the same pattern as the finished CoreDNS module:

- **Understanding:** I had it read the repo and explain the architecture (the
  single CRD, the dispatcher, and the shared `Check -> Evaluate -> Remediate`
  interface every module implements). This is where I learned what an operator
  actually is and how our four modules fit together.
- **Writing my module:** The AI generated the first version of my NetworkPolicy
  module (the `Check/Evaluate/Remediate` logic, the CRD spec fields, and unit
  tests), modelled on the existing CoreDNS module so it matched the team's style.
  I reviewed what it produced, decided what the module should actually do, and
  had it adjust things.
- **Verifying:** I ran every build, test, and lint command myself and pasted the
  results back. We fixed the lint issues the project's linter flagged until it
  was green, matching what the GitHub CI would run.
- **Learning git:** I deliberately asked it to teach me the git workflow one
  command at a time instead of doing it for me, so I understood branching,
  staging, committing, pushing, and opening a pull request. I ran every git
  command myself.

## What I decided / did vs. what the AI did
- **AI did:** wrote the module code and tests, explained Kubernetes/networking
  concepts, suggested fixes, and drafted text.
- **I did:** chose the project, coordinated with the team, decided what my module
  should do, ran all the commands on my machine, learned and executed the entire
  git and pull-request flow myself, verified everything passed CI, reviewed the
  merged result, and handled the report and this documentation.

After my module, my teammate Yuvraj reviewed the pull request and trimmed it to
just the enforcement-agent (Calico Felix) health-and-restart logic before merging
it into `main`; that trimmed version is what is in the final project.

## Why I used it
Honestly, to learn fast. The project was in a stack none of us had used, on a
tight timeline. The AI let me understand the system and contribute a working,
tested module while actually learning the concepts (operators, CNI, DNS,
NetworkPolicy, and the git/CI workflow) rather than copying blindly.

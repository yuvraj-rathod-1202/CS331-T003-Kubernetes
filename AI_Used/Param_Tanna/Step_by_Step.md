# Step-by-Step — where and how AI contributed at each stage

| Stage | What happened | AI's role | My role |
|-------|---------------|-----------|---------|
| 1. Understand the project | Learned what the project is and how the operator works | Read the repo and explained the architecture, CRD, dispatcher, and the Check->Evaluate->Remediate pattern in plain terms | Chose the project with the team; asked the questions; decided this was the direction |
| 2. Find my task | Figured out which module was mine | Inspected all branches and showed that NetworkPolicy was the only unclaimed module (no branch, still a stub) | Confirmed with the team that NetworkPolicy was mine |
| 3. Set up tooling | Installed Go 1.27 in WSL so the project could build | Gave the exact install commands and explained each line | Ran every command myself with my own password; pasted output back |
| 4. Write the module | Created `networkpolicy_types.go`, the controller (`Check/Evaluate/Remediate`), and unit tests | Wrote the first version, modelled on the finished CoreDNS module | Decided what the module should detect and remediate; reviewed the code |
| 5. Build & test | Compiled, ran unit tests, ran the linter | Ran the commands and interpreted results; fixed the lint issues the project flagged | Re-ran everything myself; confirmed 6 tests passed at ~81% coverage |
| 6. Git & PR | Branched, staged, committed, pushed, opened PR #3 | Taught me the flow one command at a time and gave PR text | Ran every git command myself; created the pull request on GitHub |
| 7. CI | GitHub CI ran build/test/lint on a clean machine | Explained what CI is and how it works in this repo | Watched the checks go green; PR #3 was reviewed and merged |
| 8. Quality check | Verified the whole merged project functions | Rebuilt/tested all modules locally (3/4 + dispatcher passed; the 4th is proven by its protected-branch merge) and reviewed teammates' modules | Asked for the check; decided what to raise with the team |
| 9. Report | Strengthened the Computer-Networks framing of the report | Added a "Networking Foundations" section (L2-L7 mapping, encapsulation, DNS/ICMP/netfilter) to the existing LaTeX without erasing content | Decided the report needed a clearer CN angle and directed the change |
| 10. This disclosure | Documented AI usage | Assembled this folder from our actual session | Reviewed it for accuracy |

## Honest summary
The AI wrote the module code and tests and explained the concepts; I made the
decisions, ran everything myself, learned and performed the whole git/CI/PR
workflow, verified the results, coordinated with the team, and handled the report
and documentation. My merged contribution in `main` is the NetworkPolicy
enforcement-agent (Calico Felix) health-check and restart logic (as trimmed by
the team lead during review).

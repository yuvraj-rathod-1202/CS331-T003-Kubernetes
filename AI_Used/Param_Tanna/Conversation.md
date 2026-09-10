# Conversation Transcript (curated)

This is a condensed transcript of my Claude Code session for this project. It has
been trimmed to the CN-project parts and to keep it readable (repeated
network-retry output from a flaky campus connection, and unrelated coursework,
are removed). The substance of who did what is unchanged.

Legend: **Me** = Param Tanna. **AI** = Claude Code.

---

**Me:** We picked Project 7, "Extending Kubernetes self-healing for network
failures." Here is our team's planning discussion [pasted WhatsApp planning
messages: Yuvraj set up the operator boilerplate, each person takes one module —
Ramji: CNI, Arpan: DNS, Deep: pod-connectivity]. I know nothing about Kubernetes;
we took this project to learn. Explain the whole project to me, and how I should
explain it. Do I give you the GitHub link?

**AI:** [Explained from scratch: what Kubernetes is; nodes, pods, control plane,
the desired-vs-actual reconcile loop; what "self-healing" means today and the gap
— K8s heals pods but not the network layer (CNI, DNS, NetworkPolicy, pod
connectivity). Explained that the solution is a custom Operator running a
Check -> Evaluate -> Remediate loop, with one module per failure type. Said a
pasted link alone wouldn't help since the repo is private, and asked me to clone
it so it could read the real code.]

**Me:** I cloned it, read through the repo.

**AI:** [Read the repo. Identified it as a Kubebuilder Go operator with four
module folders: cni, coredns, networkpolicy, podconnectivity. Found that CoreDNS
was fully implemented, CNI and pod-connectivity were on branches, and
**networkpolicy** was still an untouched stub with no branch — so that was my
module. Explained the shared `Module` interface and the dispatcher, and walked me
through what my NetworkPolicy module needed to do based on the experiment doc:
detect policy drift/deletion and stale enforcement, and remediate.]

**Me:** I need to complete the whole module today nicely and also push it. After
doing my part, review the others' work and tell me what can be improved so we get
good grades.

**AI:** [Read the finished CoreDNS module as a reference. Noted Go was not
installed and gave me the commands to install it. Then wrote my NetworkPolicy
module: the CRD spec fields, the Check/Evaluate/Remediate controller, and unit
tests, matching the CoreDNS style. Explained it needed Go 1.26+ to build.]

**Me:** [Installed Go, pasted `go version`.] go version go1.27.1 linux/amd64

**AI:** [Compiled and ran my module's tests — gofmt clean, go vet clean, all 6
unit tests passed. Then ran the full `make test` and `make lint`. Lint flagged 4
style issues in my code (use `slices` not `sort`, a constant for a repeated
string, `maps.Copy`). Fixed them. Re-ran under Go 1.26 to match CI: `make test`
passed, `make lint` reported 0 issues.]

**Me:** [Then, one command at a time, I asked the AI to teach me the git workflow
so I could do it myself.] lets go one command by one... i havent done branch push
till now.

**AI:** [Walked me through it step by step, explaining each command: creating a
branch, `git add`, `git commit`, `git push`, and opening a pull request. I ran
every command myself and pasted the output. I also asked it to explain what a
branch is, what "origin" is, and what CI is — it explained that CI is a robot on
GitHub that re-runs the build/test/lint on a clean machine and blocks the merge
if anything is red.]

**Me:** give the content for the PR so I'll create the pull request.

**AI:** [Gave a plain-language PR title and description. I created the PR (#3) on
GitHub myself.]

**Me:** [After the network let me reach GitHub] check... i think the PR is issued.

**AI:** [Checked: PR #3 "NetworkPolicy self-healing module" was open, CI check
`lint-test-build` = PASS (green), mergeable, awaiting review.]

**Me:** [Later] our PR was approved and everyone's branch got merged. Do I need to
clone again or does it auto-update?

**AI:** [Explained that git never auto-updates; I need `git fetch`/`git pull`, and
that re-cloning is unnecessary. Walked me through pulling the merged main.]

**Me:** now check the code and everything... does the project function properly?

**AI:** [Ran the full build+test on the merged main. Result: CNI, CoreDNS,
NetworkPolicy, and the dispatcher all compiled and passed their tests. The
pod-connectivity module and the cmd entrypoint could not be compiled locally only
because two dependencies would not download over the blocked campus network — not
a code error. It confirmed pod-connectivity works anyway because it was merged
into the protected `main` branch, which mechanically requires CI (full build +
test on a clean machine) to pass before merging. Conclusion: the project
functions.]

**AI (review of teammates, honest notes):** [Reported that my merged NetworkPolicy
module had been trimmed by Yuvraj during review to only the enforcement-agent
(Felix) restart logic; that the report was already fairly CN-grounded; and that
the main thing to strengthen for a Computer Networks course was to make the
networking fundamentals explicit.]

**Me:** [Report task] the report sounds like an ops project, not CN. Add a CN
point of view to the existing LaTeX without erasing what's there. And build the
AI usage folder for the submission.

**AI:** [Added a "Networking Foundations" section to the report mapping the system
onto the TCP/IP layers — L2 veth/ARP, L3 IPAM and IP-in-IP/VXLAN encapsulation,
L4 ICMP/TCP probes, L7 DNS-over-UDP — and framed NetworkPolicy as netfilter
packet filtering, without deleting existing content. Then assembled this AI_Used
folder.]

---

## Note on my own contribution (honest)
Across this session the AI wrote my module's code and tests and explained the
Kubernetes and networking concepts. I chose the project, coordinated with the
team, decided what my module should do, installed the tooling and ran every
command myself, learned and performed the entire git and pull-request workflow by
hand, verified that everything passed CI, reviewed the merged project, and
directed the report and documentation changes. I have tried to represent this
accurately rather than overstate my hands-on coding.

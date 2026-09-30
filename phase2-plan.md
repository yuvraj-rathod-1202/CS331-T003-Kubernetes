## Phase 2 Plan — Failure Model Documentation

---

### 1. CNI Failures

#### Before

**What the operator handles at this point:**
The operator monitors Calico CNI plugin health across all nodes. It watches for crashed or unresponsive CNI daemon pods, detects IP address pool exhaustion, and identifies disabled IP pools - all scenarios where Kubernetes' built-in self-healing falls short.

| Issue Name | How Our Operator Detects It | Remediation Policy |
|------------|----------------------------|-------------------|
| CNI Plugin Crash | Watches for pods stuck in `ContainerCreating` beyond a threshold duration. Also monitors Calico DaemonSet pod status via the API server for `CrashLoopBackOff` or terminated states. | **Exponential-backoff restart** of the CNI plugin pods. If restarts fail repeatedly, applies **cascading node tainting** (`NetworkUnavailable`) to prevent new scheduling on the affected node. |
| IP Pool Exhaustion | Uses Prometheus metrics / direct CNI IPAM queries to track IP pool utilization. Triggers alert when usage exceeds a configurable threshold (e.g., 90%). | Sends **threshold-based alerts** to the cluster admin. Flags the exhaustion event so operators can expand the pool or clean up stale allocations. |
| IP Pool Disabled | Periodically queries Calico `IPPool` custom resources and checks the `disabled` field. | **Auto-enables** the IP pool if found disabled, restoring pod IP allocation capability. |

#### New Additions

- Not only detect the IP pool exhaustion but also **clean up IPAM leaks** and free up resources for new pods automatically.
- 

---

### 2. CoreDNS Failures

#### Before

**What the operator handles at this point:**
The operator monitors CoreDNS pod health, DNS query latency, failure rates, and external DNS resolution. Kubernetes restarts crashed CoreDNS pods via its Deployment controller but has no awareness of DNS performance degradation, latency spikes, or upstream resolver failures.

| Issue Name | How Our Operator Detects It | Remediation Policy |
|------------|----------------------------|-------------------|
| CoreDNS Scaled to 0 | Watches the CoreDNS Deployment replica count via the API server. If available replicas drop to 0 (due to misconfiguration, accidental scale-down, or all pods crashing), the operator flags it immediately. | **Scales the CoreDNS Deployment back up** to the desired replica count. Restarts any pods stuck in `CrashLoopBackOff` or `OOMKilled` states. |
| DNS Latency Spike | Embeds a Prometheus client to scrape CoreDNS metrics (`coredns_dns_request_duration_seconds`, `coredns_dns_responses_total` with SERVFAIL/NXDOMAIN). Alerts when latency or failure rate exceeds a configurable threshold. | **Restarts** CoreDNS pods if internal metrics are degraded. If the issue persists, **reconfigures** CoreDNS with optimized cache/buffer settings. |
| External DNS Resolution Failure | Detects spikes in external DNS resolution failures via Prometheus metrics on upstream forwarding errors. | **Modifies CoreDNS ConfigMap** to switch to a backup upstream DNS server (e.g., fallback from `8.8.8.8` to `1.1.1.1`) and triggers a rolling restart of CoreDNS pods. |

#### New Additions

- 

---

### 3. NetworkPolicy Failures

#### Before

**What the operator handles at this point:**
The operator monitors NetworkPolicy enforcement by the CNI plugin (Calico). Kubernetes only provides declarative storage of NetworkPolicy resources — it does not enforce them or verify enforcement. Enforcement is entirely delegated to the CNI plugin. If the CNI fails to program iptables/eBPF rules or a policy is misconfigured, Kubernetes is unaware.

| Issue Name | How Our Operator Detects It | Remediation Policy |
|------------|----------------------------|-------------------|
| NetworkPolicy Not Enforced | Runs periodic **connectivity probes** between pods that should be blocked by a NetworkPolicy. If traffic that should be denied is allowed, it flags an enforcement failure. Also checks Calico Felix agent health. | **Reapplies** the NetworkPolicy resource to trigger re-programming by the CNI. If Calico Felix is unhealthy, **restarts** the Felix agent pod on the affected node. |
| Misconfigured NetworkPolicy (blocking legitimate traffic) | Monitors for services that become unreachable after a NetworkPolicy change by correlating policy update events with connectivity probe failures. | **Alerts** the cluster admin with details of the suspected misconfiguration. Optionally, **rolls back** to the previous known-good NetworkPolicy if a backup was stored. |

#### New Additions

improve it and test it completely.

---

### 4. Pod-to-Pod Connectivity Failures

#### Before

**What the operator handles at this point:**
The operator implements periodic network probes to verify pod-to-pod and internode connectivity. Kubernetes has no built-in mechanism to detect or recover from network-layer failures between pods — it only knows if a pod process is alive (via liveness probes), not if the pod can reach other pods over the network.

| Issue Name | How Our Operator Detects It | Remediation Policy |
|------------|----------------------------|-------------------|
| Intra-node Pod-to-Pod Connectivity Loss | Deploys lightweight **probe pods** (or uses existing workloads) to send periodic test requests (TCP/ICMP) between pods on the same node. If responses fail or timeout, it flags a connectivity issue. | **Restarts** the CNI agent pod on the affected node to restore local networking. If the issue persists, **taints** the node and **alerts** the admin. |
| Internode Connectivity Failure | Sends periodic cross-node probes between pods on different nodes. Also monitors node network interfaces and routing table health via node-level checks. | **Restarts** the CNI agent pod on the affected node(s). If routing is broken, attempts to **re-trigger CNI route advertisement**. If unresolvable, **cordons** the affected node and **alerts** the admin. |

#### New Additions

- What if veth of the pod is down
- why O(n^2) for naive solution
- how are we validating the pod to pod connectivity in the inter node and what is check.
- why node to node check with naive solution is not O(n^2)
- 
## Phase 2 Plan

### 1. CNI Failures

#### Before

- CNI Plugin Crash 
- IP Pool Exhaustion detection
- IP Pool Disabled

#### New Additions

- Not only detect the IP pool exhaustion but also clean up IPAM leaks and free up resources for new pods automatically.
- Proper Exponential Backoff for CNI Restarts.
- Automatic IPPool Expansion if pool gets exhausted.


### 2. CoreDNS Failures

#### Before

- CoreDNS Scaled to 0
- DNS Latency Spike
- External DNS Resolution Failure

#### New Additions

- Auto Scaling Based on Cluster Load: auto-scale CoreDNS based on the number of nodes.
- scale to 0 is not part of standar kubernetes, there are some addons like KEDA/Knative which enable to scale to 0 and auto scale that's why we handled it.

### 3. NetworkPolicy Failures

#### Before

- NetworkPolicy Not Enforced
- Misconfigured NetworkPolicy (blocking legitimate traffic)

#### New Additions

- improve it and test it completely.

### 4. Pod-to-Pod Connectivity Failures

#### Before

- Intra-node Pod-to-Pod Connectivity Loss
- Internode Connectivity Failure

#### New Additions

- inter-node from O(2N) to O(N): directed instead of undirected topology.
- deamonsets in each of the pod for inter node probes for inter-node pod to pod.
- pick 2 pods on the same node and have one ping other's podIP. 
- sample 2-3 pods per node to improve the coverage without blowing up to N
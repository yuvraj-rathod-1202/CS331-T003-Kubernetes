// Results of operator/cmd/ipam-sim (docs/ipam-eval/results.md and sweep.csv), mean of 3 seeds.
export const policies = ['buddy', 'static-/24', 'static-/25', 'calico-64'] as const;
export type PolicyName = (typeof policies)[number];

export interface ScenarioRow { scenario: string; note: string; served: number[]; util: number[]; addrs: number[] }

// order of values follows `policies`
export const scenarios: ScenarioRow[] = [
  { scenario: 'steady', note: '50 nodes × 80 pods', served: [100, 100, 100, 100], util: [62.5, 31.2, 62.5, 62.5], addrs: [128, 256, 128, 128] },
  { scenario: 'skewed', note: '10% of nodes at 220 pods, rest 30', served: [100, 100, 80.6, 100], util: [56.7, 19.1, 30.9, 58.9], addrs: [86, 256, 128, 83] },
  { scenario: 'burst', note: 'all nodes 20 → 200 → 20 pods', served: [100, 100, 73.1, 100], util: [35.0, 32.0, 46.8, 38.2], addrs: [218, 256, 128, 217] },
  { scenario: 'wave', note: 'per-node sine, random phase', served: [100, 100, 96.6, 100], util: [40.9, 27.8, 53.7, 52.4], addrs: [175, 256, 128, 137] },
  { scenario: 'tight pool', note: '/20 shared by 40 nodes', served: [100, 57.1, 72.3, 100], util: [64.8, 31.2, 39.6, 67.3], addrs: [86, 102, 102, 83] },
];

// pods served (%) as the cluster grows: 60 pods per node in a /16
export const sweep = {
  nodes: [200, 400, 600, 800, 1000],
  served: {
    'buddy': [100, 100, 100, 100, 100],
    'static-/24': [100, 64, 42.7, 32, 25.6],
    'static-/25': [100, 100, 85.3, 64, 51.2],
    'calico-64': [100, 100, 100, 100, 100],
  } as Record<PolicyName, number[]>,
};

// Checked on a 3-node minikube cluster (see docs/ipam-agent.md).
export const liveChecks: { title: string; result: string }[] = [
  { title: 'Initial blocks', result: 'Pending → Active on their own, each acknowledged by the other two nodes; all 6 cross-node pod pairs answered ping.' },
  { title: 'Grow in place', result: 'A node doubled its /24; the agent wrote one /23 range, one route covered it, and a pod got 10.244.2.255 (a broadcast address before the merge).' },
  { title: 'Shrink', result: 'A block drained, was quarantined and released with no manual step; its route was withdrawn on the other nodes.' },
  { title: 'Leak reclaim', result: '3 leaked leases older than 2 min were removed; the lease of a running pod was kept.' },
  { title: 'Emergency grow', result: 'Agent paused, block full: a pod failed with "no IP addresses available" and the controller added a block from the kubelet event.' },
  { title: 'Stale NAT', result: 'An injected CNI-* MASQUERADE rule and chain were removed within a minute.' },
  { title: 'Reaction time', result: '0.6 s from a new NodeBlock to its route acknowledged on both other nodes (watch events).' },
];

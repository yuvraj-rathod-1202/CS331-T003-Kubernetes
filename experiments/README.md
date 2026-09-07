# Experiments

This directory is used to experiment with and validate the networking failure scenarios identified in the project. Before building the custom operator, each failure mode (CNI crashes, CoreDNS degradation, NetworkPolicy misconfigurations, pod connectivity issues) needs to be manually reproduced and understood in a real cluster.

The experiments here serve to:

- **Reproduce failures** - Simulate each identified issue in a controlled Minikube environment to confirm it behaves as expected.
- **Validate detection signals** - Identify what metrics, pod statuses, or events reliably indicate a failure has occurred.
- **Test remediation steps** - Manually perform the recovery actions (restarting pods, reapplying policies, etc.) to verify they actually resolve the issue before automating them in the operator.
- **Document findings** - Record observations, edge cases, and gotchas discovered during experimentation.

## Structure

| File / Directory | Description |
|---|---|
| `setup.md` | Environment setup instructions for running the experiments |
| `cni-failure.md` | Experiment for CNI plugin failure scenarios |
| `coredns-failure.md` | Experiment for CoreDNS degradation and DNS resolution failures |
| `networkpolicy-failure.md` | Experiment for NetworkPolicy enforcement issues |
| `pod-connectivity-failure.md` | Experiment for pod-to-pod and internode connectivity failures |
| `manifests/` | Kubernetes manifests used across the experiments |

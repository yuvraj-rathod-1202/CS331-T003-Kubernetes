# Project Description

Kubernetes provides built-in self-healing features like restarting failed pods or rescheduling workloads when nodes go down However, it does not automatically recover from networking-related failures such as CNI plugin crashes, CoreDNS issues, or broken NetworkPolicies.

In this project, a custom Kubernetes operator/controller will be developed to detect and fix such failures. The operator will monitor:

CNI plugin health (Calico pods)
DNS health (CoreDNS latency and failures)
Pod-to-pod connectivity (via periodic probes)
NetworkPolicy enforcement (detect unreachable services)

When a problem is detected, the operator will apply automated remediation  e.g., restarting pods, reapplying NetworkPolicies, or switching to a backup DNS configuration.

Tools:

Kubernetes (Minikube)
Prometheus for health metrics + Alertmanager
Go (Kubebuilder) for operator development
Calico as CNI plugin
Loki/Grafana for log monitoring (optional)

Expected Learning Outcomes:

Understand limits of Kubernetes’ built-in self-healing
Learn how to extend Kubernetes with custom controllers
Debug and recover networking failures in clusters
Gain hands-on experience with Kubernetes monitoring and networking stack

# Current limitations of kubernetes self-healing

### CNI Failures

Kubernetes does not automatically recover from CNI plugin failures in the same way it restarts standard pods. kubernetes does not auto-heal broken underlying CNI plugins. It does execute specific feedback logic.
- Mark Node as NotReady
- Restarts the CNI plugin pods (if they are managed by a DaemonSet)
- Block new pod creation on that node. Pods stick in ContainerCreating state
- Once the CNI plugin is back up, the node is marked as Ready and new pods can be scheduled.

If the CNI plugin crashes or becomes unresponsive, pods may lose network connectivity, and new pods may fail to start. 

| Action | Kubernetes Behavior |
|--------|-------------------|
| Detecting CNI Plugin Crash | Node marked NotReady, new pods stuck in ContainerCreating |
| Detecting IP pool exhaustion | No, Native K8s does not manage IP ranges, pods will just fail to start |

The operator will monitor the health of the CNI plugin and restart it if necessary.

### CoreDNS Failures

CoreDNS is responsible for DNS resolution in the cluster. Failures in CoreDNS can lead to service discovery issues and affect the overall functionality of the cluster.

| Action | Kubernetes Behavior |
|--------|-------------------|
| Self-heal CoreDNS degradation | Create/Restart failing pods |
| Detecting CoreDNS Latency or Failures | No automatic detection |
| Restoring DNS Resolution | No automatic recovery |

The operator will monitor CoreDNS health and take corrective actions when issues are detected.

### NetworkPolicy Failures

When it comes to NetworkPolicies, Kubernetes acts purely declarative configuration storage system, not an enforcement mechanism. It accepts YAML and passes it to the CNI. Kubernetes handles Schema Validation, Resource Lifecycle and API Delivery, but it does not enforce the policies. Enforcement is done by the CNI plugin (e.g., Calico). If a NetworkPolicy is misconfigured or if the CNI plugin fails to enforce it, Kubernetes does not automatically recover from these issues.

| Action | Kubernetes Behavior |
|--------|-------------------|
| Restarting a Crashed NetworkPolicy Pod | restart failing pod |

The operator will monitor NetworkPolicy enforcement and take corrective actions when issues are detected.

### Pod-to-Pod Connectivity Failures

Pod-to-pod connectivity is crucial for the proper functioning of applications in a Kubernetes cluster. If pods cannot communicate with each other due to network issues, it can lead to application failures.

| Action | Kubernetes Behavior |
|--------|-------------------|
| Detecting Pod-to-Pod Connectivity Issues | No automatic detection |
| internode Connectivity Failures | No automatic recovery |

The operator will implement periodic probes to check pod-to-pod connectivity and take corrective actions when issues are detected.

# Handling Failures

### CNI Plugin Failures

1. Detecting CNI plugin crashes and failures
    The core strategy used by custom contorller includer:
    - Exponential backoff for retries: When a CNI plugin crash is detected, the operator will attempt to restart the CNI plugin pods with an exponential backoff strategy. This means that if the first restart attempt fails, the operator will wait for a short period before trying again, and if it fails again, it will wait for a longer period before the next attempt. This helps to avoid overwhelming the system with rapid restart attempts.
    - Cascading Node Tainting: If the CNI plugin continues to fail after multiple restart attempts, the operator will taint the affected node to prevent new pods from being scheduled on it. This ensures that the node is not overloaded with new workloads while the CNI plugin is unstable.
    - IPAM Leak Detection: The operator will monitor the IP address allocation for pods and detect any leaks or exhaustion of the IP pool. If an IP leak is detected, the operator will sent an alert.
    - Self-Healing DaemonSet Restarts: The operator will monitor the health of the CNI plugin DaemonSet and automatically restart any failed pods. This ensures that the CNI plugin is always running and available for new pod scheduling.

    Operator will watch the API for any pod resource stuck in ContainerCreating state for a certain period of time. If a pod is stuck in this state, it indicates that the CNI plugin may be down or unresponsive. The operator will then trigger the remediation process to restart the CNI plugin and resolve the issue.

2. Recovering from IP pool exhaustion
    The operator will monitor the IP address allocation for pods and detect any exhaustion of the IP pool. If the IP pool is exhausted, the operator will clean up leaked IP allocations and free up resources for new pods.

    Operator uses Prometheus client to track the metric or queries the CNI plugin for the current IP pool usage. If the usage exceeds a certain threshold, the operator will trigger the cleanup process to free up IP addresses.

Flow of CNI plugin failure detection and remediation:

1. Watcher Loops: The operator continuously watches for events related to CNI plugin pods and nodes. It listens for pod status changes, node taints, and other relevant events.

2. Evaluator: Confirms the failure by checking the status of the CNI plugin pods and nodes. 

3. Remediation Trigger: If a failure is confirmed, the operator triggers the remediation process, which may include restarting pods, tainting nodes, cleaning up IP allocations, and restoring missing binaries.

### CoreDNS Failures

1. Detecting CoreDNS Degradation
    The operator will monitor the health of CoreDNS pods by checking their status and resource usage. If a CoreDNS pod is found to be in a failed state or experiencing high resource usage, the operator will trigger the remediation process to restart the affected pods.

2. Detecting CoreDNS Latency or Failures
    Operator will embed prometheus client to monitor DNS query latency and failure rates. If matrics exceed a certain threshold, the operator will trigger the remediation process to restart CoreDNS pods or switch to a backup DNS configuration.

3. Restoring DNS Resolution
    Problem: local CoreDNS is fine, but public DNS resolvers are down.

    When operator detects spike in failures in external DNS resolution, It will modify the CoreDNS configuration to use a backup DNS server or reconfigure upstream settings to ensure DNS resolution continues to function.

### NetworkPolicy Failures

1. Detecting NetworkPolicy Enforcement Issues
    The operator will monitor the enforcement of NetworkPolicies by checking the status of the CNI plugin and the applied policies. If a NetworkPolicy is found to be misconfigured or not enforced correctly, the operator will trigger the remediation process to reapply the policy or restart the affected pods.

### Pod-to-Pod Connectivity Failures

1. Detecting Pod-to-Pod Connectivity Issues
    The operator will implement periodic probes to check pod-to-pod connectivity. It will send test requests between pods and monitor the responses. If a pod fails to respond or if there are connectivity issues, the operator will trigger the remediation process.

2. Internode Connectivity Failures
    The operator will also monitor internode connectivity by checking the status of the network interfaces and routing tables. If a node is found to be unreachable or if there are routing issues, the operator will trigger the remediation process to restart the CNI agent pod and alert if that does not resolve the issue.
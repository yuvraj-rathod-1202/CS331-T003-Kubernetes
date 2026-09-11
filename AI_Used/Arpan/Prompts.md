# Prompts

## Setting up and understanding the project

1. Initial setup prompt asking the AI to help implement the CoreDNS remediation module for the Kubernetes self-healing operator project — described the project goals, my assigned module, and the expected architecture.

2. Asked the AI to review the project description and verify what features the CoreDNS module needed to implement, to ensure nothing was missed.

## Building the CoreDNS module

3. Asked the AI to generate the CoreDNS controller (`controller.go`) with the reconciliation loop that watches CoreDNS pod health and triggers remediation.

4. Asked the AI to continue and complete the implementation after reviewing the initial scaffolding.

5. Provided operator logs showing failures and asked the AI to debug why CoreDNS degradation was not being detected properly.

6. Asked the AI to fix issues with the remediation logic — pods were not being restarted or rescheduled correctly after DNS failure detection.

## Code quality and review

7. Asked the AI to review the code for race conditions and fix any concurrency issues in the controller logic.

8. Asked the AI to run lint checks and fix all lint errors across the codebase.

## K8s-Visualizer fixes

9. Reported simulation bugs in the k8s-visualizer React frontend — the CoreDNS remediation events were not displaying correctly in the dashboard.

10. Asked the AI to fix the visualizer to properly show CoreDNS pod states and remediation actions.

11. Asked the AI to test the web app behavior and verify the visual output matched expectations.

## Documentation

12. Asked the AI to generate documentation for the CoreDNS remediation module, including a workflow diagram explaining the remediation pipeline.

13. Asked the AI to add the documentation markdown file (`operator/docs/coredns-remediation.md`) to the project.

## Verification

14. Asked the AI to compare the implemented features against the full project description and flag any missing functionality.
/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package module defines the interface that all remediation modules must implement.
// The operator follows a Check → Evaluate → Remediate pipeline for each module.
// See docs/architecture.md for a detailed explanation of this pattern.
package module

import (
	"context"

	remediationv1alpha1 "CS331-CN-Project-1/operator/api/v1alpha1"
)

// CheckResult holds the raw signals gathered during the Check phase.
// Each module populates Signals with its own domain-specific data.
type CheckResult struct {
	// Signals is a map of signal names to their values.
	// Each module defines its own signal keys (e.g., "containerCreatingPods", "dnsLatencyMs").
	Signals map[string]any
	// Err is any error encountered during the check phase (nil if check succeeded).
	Err error
}

// EvalResult holds the evaluation outcome after analyzing check results.
type EvalResult struct {
	// IsHealthy is true if no issue was found.
	IsHealthy bool
	// NeedsRemediation is true if the Remediate phase should run.
	NeedsRemediation bool
	// Reason is a human-readable explanation of what was found.
	Reason string
	// Severity indicates how critical the issue is ("info", "warning", "critical").
	Severity string
}

// RemediateResult holds the outcome of a remediation action.
type RemediateResult struct {
	// Action describes what was done (e.g., "restarted calico-node on node-2").
	Action string
	// Success is true if remediation succeeded.
	Success bool
	// Err is any error from the remediation (nil if it succeeded).
	Err error
}

// Module is the interface that every remediation module must implement.
// Each team member implements this for their assigned module (CNI, CoreDNS,
// NetworkPolicy, or PodConnectivity).
//
// The three methods form a pipeline:
//  1. Check - Gather raw health signals (pod statuses, metrics, probe results).
//     No decisions are made here.
//  2. Evaluate - Analyze the signals to determine if there is an issue.
//     Classify severity and decide if remediation is needed.
//  3. Remediate - Execute the corrective action. Only called when
//     Evaluate returns NeedsRemediation=true.
type Module interface {
	// Name returns the module name (e.g., "cni", "coredns", "networkpolicy", "podconnectivity").
	Name() string

	// Check gathers raw health signals for this module.
	// This is the data-gathering phase - no decisions are made here.
	Check(ctx context.Context, spec *remediationv1alpha1.NetworkRemediationSpec) (*CheckResult, error)

	// Evaluate analyzes the check results to determine if there is an issue.
	// Returns whether remediation is needed and why.
	Evaluate(ctx context.Context, checkResult *CheckResult) (*EvalResult, error)

	// Remediate executes corrective actions based on the evaluation.
	// Only called when Evaluate returns NeedsRemediation=true.
	Remediate(ctx context.Context, evalResult *EvalResult) (*RemediateResult, error)
}

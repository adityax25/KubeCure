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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AnalyzerConfig selects how root cause analysis is performed.
type AnalyzerConfig struct {
	// Provider is the preferred analyzer. The rule engine requires no credentials and no network.
	// +kubebuilder:validation:Enum=rules;gemini
	// +kubebuilder:default=rules
	Provider string `json:"provider,omitempty"`

	// EscalateToProvider names an analyzer consulted when the rule engine finds no confident match.
	// Empty disables escalation, leaving unmatched failures without a root cause rather than a
	// guessed one.
	// +optional
	// +kubebuilder:validation:Enum=gemini;""
	EscalateToProvider string `json:"escalateToProvider,omitempty"`

	// MinRuleConfidence is the confidence below which a rule result is treated as inconclusive and
	// escalation is considered.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=70
	MinRuleConfidence int32 `json:"minRuleConfidence,omitempty"`
}

// AgentConfig bounds the investigation loop. Every limit is independent, and the loop stops on
// whichever is reached first, so an investigation can neither run indefinitely nor spend without a
// ceiling.
type AgentConfig struct {
	// Enabled turns on goal directed investigation. When false, analysis is limited to the
	// deterministic rule engine, which requires no credentials and no network access.
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// MaxToolCalls caps how many reads a single investigation may perform.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:default=12
	MaxToolCalls int32 `json:"maxToolCalls,omitempty"`

	// MaxTokens caps model token consumption for a single investigation, summed across every turn.
	// Each turn resends the conversation so far, so consumption grows faster than the number of
	// turns, and the default allows for a full investigation rather than a single call.
	// +optional
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:default=100000
	MaxTokens int32 `json:"maxTokens,omitempty"`

	// MaxCostMicroUSD caps spend per investigation in millionths of a dollar.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=50000
	MaxCostMicroUSD int64 `json:"maxCostMicroUSD,omitempty"`

	// TimeoutSeconds caps wall clock time for a single investigation.
	// +optional
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=600
	// +kubebuilder:default=60
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// JudgeEnabled turns on review of hypotheses against their cited evidence before any action is
	// considered. Disabling it removes the check that catches unsupported conclusions.
	// +optional
	// +kubebuilder:default=true
	JudgeEnabled bool `json:"judgeEnabled,omitempty"`
}

// RemediationConfig governs whether and how a proposed action is applied.
type RemediationConfig struct {
	// Mode selects the remediation strategy. Dry run is the default so that a fresh install
	// observes and proposes without changing anything.
	// +kubebuilder:default=DryRun
	Mode RemediationMode `json:"mode,omitempty"`

	// MinConfidence is the confidence required before an action is applied. Proposals below the
	// threshold are recorded and left for human review.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=85
	MinConfidence int32 `json:"minConfidence,omitempty"`

	// AllowedActions restricts which action types may be applied in this namespace. An action not
	// listed here is recorded but never executed, regardless of confidence.
	// +optional
	AllowedActions []ActionType `json:"allowedActions,omitempty"`

	// MaxBlastRadius is the widest scope of change permitted here. Candidates affecting more than
	// this are recorded but never applied, regardless of how confident the diagnosis is.
	// +optional
	// +kubebuilder:default=Workload
	MaxBlastRadius BlastRadius `json:"maxBlastRadius,omitempty"`

	// RequireReversible restricts automatic application to changes that can be undone. Confidence
	// measures certainty about the cause; this bounds the cost of being wrong about it.
	// +optional
	// +kubebuilder:default=true
	RequireReversible bool `json:"requireReversible,omitempty"`

	// VerificationWindow is how long a remediated workload must stay healthy before the diagnosis
	// is considered resolved.
	// +optional
	// +kubebuilder:default="2m"
	VerificationWindow metav1.Duration `json:"verificationWindow,omitempty"`
}

// HealingPolicySpec is the configuration governing how failures are handled in one namespace.
type HealingPolicySpec struct {
	// FailureTypes limits handling to the listed failure modes. Empty means all supported modes.
	// +optional
	FailureTypes []FailureType `json:"failureTypes,omitempty"`

	// Analyzer configures root cause analysis.
	// +optional
	Analyzer AnalyzerConfig `json:"analyzer,omitempty"`

	// Agent bounds the investigation loop.
	// +optional
	Agent AgentConfig `json:"agent,omitempty"`

	// Remediation configures whether and how fixes are applied.
	// +optional
	Remediation RemediationConfig `json:"remediation,omitempty"`

	// MaxDiagnosesPerHour caps how many new diagnoses may be opened in this namespace per hour,
	// bounding both analysis cost and object growth when a cluster is broadly unhealthy.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=20
	MaxDiagnosesPerHour int32 `json:"maxDiagnosesPerHour,omitempty"`

	// ResolvedRetention is how long resolved diagnoses are kept before deletion.
	// +optional
	// +kubebuilder:default="24h"
	ResolvedRetention metav1.Duration `json:"resolvedRetention,omitempty"`
}

// HealingPolicyStatus is the observed state of a HealingPolicy.
type HealingPolicyStatus struct {
	// ActiveDiagnoses is the number of diagnoses in this namespace that have not reached a terminal
	// phase.
	// +optional
	ActiveDiagnoses int32 `json:"activeDiagnoses,omitempty"`

	// DiagnosesThisHour is the count used to enforce MaxDiagnosesPerHour.
	// +optional
	DiagnosesThisHour int32 `json:"diagnosesThisHour,omitempty"`

	// Conditions carries standard condition entries.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=hpol
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.remediation.mode`
// +kubebuilder:printcolumn:name="MinConf",type=integer,JSONPath=`.spec.remediation.minConfidence`
// +kubebuilder:printcolumn:name="Analyzer",type=string,JSONPath=`.spec.analyzer.provider`
// +kubebuilder:printcolumn:name="Agent",type=boolean,JSONPath=`.spec.agent.enabled`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeDiagnoses`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HealingPolicy configures how KubeCure handles failures within its namespace.
type HealingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HealingPolicySpec   `json:"spec,omitempty"`
	Status HealingPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HealingPolicyList contains a list of HealingPolicy.
type HealingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HealingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HealingPolicy{}, &HealingPolicyList{})
}

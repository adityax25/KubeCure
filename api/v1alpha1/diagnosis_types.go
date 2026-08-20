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
	"k8s.io/apimachinery/pkg/types"
)

// Phase is the stage a Diagnosis has reached in the detect, enrich, analyze, remediate pipeline.
// Each controller acts on exactly one phase and advances the object to the next, so the phase also
// serves as the guard that prevents costly work from being repeated on requeue.
// +kubebuilder:validation:Enum=Detected;Enriched;Diagnosed;Remediating;Healed;Failed;AwaitingHuman
type Phase string

const (
	// PhaseDetected means the failure has been classified but no evidence has been gathered.
	PhaseDetected Phase = "Detected"
	// PhaseEnriched means logs, events, and owner context have been collected and redacted.
	PhaseEnriched Phase = "Enriched"
	// PhaseDiagnosed means a root cause and a proposed action have been recorded.
	PhaseDiagnosed Phase = "Diagnosed"
	// PhaseRemediating means the action has been applied and recovery is being verified.
	PhaseRemediating Phase = "Remediating"
	// PhaseHealed means the target recovered and remained stable through the verification window.
	PhaseHealed Phase = "Healed"
	// PhaseFailed means remediation was attempted and the target did not recover.
	PhaseFailed Phase = "Failed"
	// PhaseAwaitingHuman means no safe automated action exists, or a change awaits review.
	PhaseAwaitingHuman Phase = "AwaitingHuman"
)

// TargetRef identifies the failing pod. The UID is recorded because pod names are reused, and a
// diagnosis must remain bound to the specific instance that failed.
type TargetRef struct {
	// Name is the pod name.
	Name string `json:"name"`

	// UID is the pod's unique identifier.
	UID types.UID `json:"uid"`

	// Owner is the workload that manages the pod, and is where a fix is normally applied. Pods
	// created directly have no owner.
	// +optional
	Owner *OwnerRef `json:"owner,omitempty"`
}

// OwnerRef identifies the workload controlling the failing pod.
type OwnerRef struct {
	// Kind is the owning workload kind, such as Deployment or StatefulSet.
	Kind string `json:"kind"`

	// Name is the owning workload name.
	Name string `json:"name"`
}

// DiagnosisSpec records the observed facts of a single failure. It is written once when the failure
// is detected and is never modified afterwards; all subsequent progress is recorded in the status.
type DiagnosisSpec struct {
	// Target is the pod that failed.
	Target TargetRef `json:"target"`

	// FailureType is the classification assigned by the detector.
	FailureType FailureType `json:"failureType"`

	// Container names the failing container within the pod. Empty for pod level failures such as
	// eviction or unschedulability.
	// +optional
	Container string `json:"container,omitempty"`

	// Signature is a hash over namespace, owner, container, and failure type. Object names derive
	// from it, so replicas failing identically collapse into a single diagnosis.
	Signature string `json:"signature"`

	// ObservedAt is when the detector first saw the failure.
	ObservedAt metav1.Time `json:"observedAt"`

	// PolicyRef names the HealingPolicy in this namespace that governs handling. Empty means no
	// policy was found, in which case the diagnosis is recorded but no remediation is attempted.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`
}

// Timings records when each pipeline stage completed. These fields are the source for all reported
// durations, so every published latency figure traces back to values the operator wrote itself.
type Timings struct {
	// +optional
	DetectedAt *metav1.Time `json:"detectedAt,omitempty"`
	// +optional
	EnrichedAt *metav1.Time `json:"enrichedAt,omitempty"`
	// +optional
	DiagnosedAt *metav1.Time `json:"diagnosedAt,omitempty"`
	// +optional
	RemediatedAt *metav1.Time `json:"remediatedAt,omitempty"`
	// +optional
	HealedAt *metav1.Time `json:"healedAt,omitempty"`
}

// Evidence is the diagnostic context gathered for a failure. Content is captured at collection time
// rather than referenced, because a failing pod may be deleted before analysis runs, at which point
// its logs are unrecoverable.
type Evidence struct {
	// Logs holds the tail of the failing container's output, taken from the previous instance when
	// the container has already restarted. Truncated to stay well inside object size limits.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Logs string `json:"logs,omitempty"`

	// Events holds warning events for the pod, most recent first, one per line.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Events string `json:"events,omitempty"`

	// ExitCode is the previous instance's exit status, when the container terminated.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// RestartCount is the container's restart count at collection time.
	// +optional
	RestartCount int32 `json:"restartCount,omitempty"`

	// TerminationReason is the reason reported for the previous termination, such as OOMKilled.
	// +optional
	TerminationReason string `json:"terminationReason,omitempty"`

	// WaitingReason is the reason the container is currently not running, such as CrashLoopBackOff.
	// +optional
	WaitingReason string `json:"waitingReason,omitempty"`

	// RedactedFields counts values removed by the redactor before the evidence was stored. A non
	// zero count means sensitive material was present and did not leave the cluster.
	// +optional
	RedactedFields int32 `json:"redactedFields,omitempty"`
}

// Analysis is the root cause conclusion and the change proposed to resolve it.
type Analysis struct {
	// Provider names the analyzer that produced this result, such as rules or an LLM backend.
	Provider string `json:"provider"`

	// RuleID identifies the matching rule when the rule engine produced the result.
	// +optional
	RuleID string `json:"ruleID,omitempty"`

	// RootCause explains the failure in a form suitable for a pull request description.
	// +kubebuilder:validation:MaxLength=2048
	RootCause string `json:"rootCause"`

	// ConfidencePercent expresses certainty from 0 to 100. An integer is used deliberately, since
	// the Kubernetes API conventions discourage floating point fields.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ConfidencePercent int32 `json:"confidencePercent"`

	// Action is the proposed remediation. A type of None means no safe automated fix exists.
	Action Action `json:"action"`

	// TokensUsed records model token consumption, and is zero for the rule engine.
	// +optional
	TokensUsed int32 `json:"tokensUsed,omitempty"`
}

// Remediation records what was attempted and whether the target recovered.
type Remediation struct {
	// Mode is the remediation strategy that applied.
	Mode RemediationMode `json:"mode"`

	// Applied reports whether a change was actually made. False in dry run mode, and false when the
	// confidence gate or the allowed action list rejected the proposal.
	Applied bool `json:"applied"`

	// SkipReason explains why no change was made, when Applied is false.
	// +optional
	SkipReason string `json:"skipReason,omitempty"`

	// Patch is the computed change in a human readable form. Recorded in every mode, including dry
	// run, so the proposal can be reviewed without applying it.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Patch string `json:"patch,omitempty"`

	// Reference points at the resulting artifact, such as a pull request URL.
	// +optional
	Reference string `json:"reference,omitempty"`

	// Verified reports the outcome of the recovery check. Nil means verification has not completed.
	// +optional
	Verified *bool `json:"verified,omitempty"`
}

// DiagnosisStatus is the observed progress of a diagnosis. Only controllers write it.
type DiagnosisStatus struct {
	// Phase is the current pipeline stage.
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// Conditions carries standard condition entries for detailed state and error reporting.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Timings records stage completion times.
	// +optional
	Timings Timings `json:"timings,omitempty"`

	// TimeToDiagnosis is the interval from detection to a recorded root cause, formatted for
	// display. Derived from Timings and stored so it can be surfaced as a printer column.
	// +optional
	TimeToDiagnosis string `json:"timeToDiagnosis,omitempty"`

	// TimeToRecovery is the interval from detection to verified recovery, formatted for display.
	// Populated only in direct mode, since pull request mode depends on human review the operator
	// does not control.
	// +optional
	TimeToRecovery string `json:"timeToRecovery,omitempty"`

	// Evidence is the gathered diagnostic context.
	// +optional
	Evidence *Evidence `json:"evidence,omitempty"`

	// Analysis is the root cause conclusion and proposed action.
	// +optional
	Analysis *Analysis `json:"analysis,omitempty"`

	// Remediation is the record of what was attempted.
	// +optional
	Remediation *Remediation `json:"remediation,omitempty"`

	// ObservedGeneration is the spec generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=diag
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.failureType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Conf",type=integer,JSONPath=`.status.analysis.confidencePercent`
// +kubebuilder:printcolumn:name="MTTD",type=string,JSONPath=`.status.timeToDiagnosis`
// +kubebuilder:printcolumn:name="MTTR",type=string,JSONPath=`.status.timeToRecovery`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Diagnosis is a single detected pod failure and the record of how it was handled.
type Diagnosis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DiagnosisSpec   `json:"spec,omitempty"`
	Status DiagnosisStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DiagnosisList contains a list of Diagnosis.
type DiagnosisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Diagnosis `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Diagnosis{}, &DiagnosisList{})
}

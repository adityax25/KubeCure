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
// +kubebuilder:validation:Enum=Detected;Enriched;Hypothesized;Judged;Planned;Remediating;Verifying;Healed;Failed;AwaitingHuman
type Phase string

const (
	// PhaseDetected means the failure has been classified but no evidence has been gathered.
	PhaseDetected Phase = "Detected"
	// PhaseEnriched means logs, events, and owner context have been collected and redacted.
	PhaseEnriched Phase = "Enriched"
	// PhaseHypothesized means the investigation produced ranked explanations, not yet scrutinised.
	PhaseHypothesized Phase = "Hypothesized"
	// PhaseJudged means each hypothesis has been checked against the evidence it cites.
	PhaseJudged Phase = "Judged"
	// PhasePlanned means a remediation has been selected but nothing has been applied. This is the
	// boundary at which a human reviews a proposal.
	PhasePlanned Phase = "Planned"
	// PhaseRemediating means the selected action is being applied.
	PhaseRemediating Phase = "Remediating"
	// PhaseVerifying means the action was applied and recovery is being observed.
	PhaseVerifying Phase = "Verifying"
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
	HypothesizedAt *metav1.Time `json:"hypothesizedAt,omitempty"`
	// +optional
	JudgedAt *metav1.Time `json:"judgedAt,omitempty"`
	// +optional
	PlannedAt *metav1.Time `json:"plannedAt,omitempty"`
	// +optional
	RemediatedAt *metav1.Time `json:"remediatedAt,omitempty"`
	// +optional
	HealedAt *metav1.Time `json:"healedAt,omitempty"`
}

// EvidenceKind names a category of diagnostic context. Requirements are declared per failure type,
// so what is gathered depends on what the failure actually needs.
// +kubebuilder:validation:Enum=PreviousLogs;CurrentLogs;Events;WorkloadSpec;RolloutHistory;ConfigReferences;ServiceEndpoints;NodeStatus;SiblingPods;ResourceUsage
type EvidenceKind string

const (
	// EvidencePreviousLogs is output from the container instance that died, which is the only place
	// a crash looping container records why it stopped.
	EvidencePreviousLogs EvidenceKind = "PreviousLogs"
	// EvidenceCurrentLogs is output from the running instance.
	EvidenceCurrentLogs EvidenceKind = "CurrentLogs"
	// EvidenceEvents is the pod's warning events, which name missing objects and scheduler verdicts.
	EvidenceEvents EvidenceKind = "Events"
	// EvidenceWorkloadSpec is the owning workload's pod template, carrying limits, probes, and image.
	EvidenceWorkloadSpec EvidenceKind = "WorkloadSpec"
	// EvidenceRolloutHistory is recent revisions of the workload, which establishes whether a change
	// shortly preceded the failure.
	EvidenceRolloutHistory EvidenceKind = "RolloutHistory"
	// EvidenceConfigReferences records whether referenced ConfigMaps and Secrets exist. Keys are
	// listed; values are never read.
	EvidenceConfigReferences EvidenceKind = "ConfigReferences"
	// EvidenceServiceEndpoints is the Services selecting this pod and their endpoint membership.
	EvidenceServiceEndpoints EvidenceKind = "ServiceEndpoints"
	// EvidenceNodeStatus is the host node's conditions and allocatable capacity.
	EvidenceNodeStatus EvidenceKind = "NodeStatus"
	// EvidenceSiblingPods is the state of other pods in the same workload, distinguishing a single
	// bad pod from a workload wide fault.
	EvidenceSiblingPods EvidenceKind = "SiblingPods"
	// EvidenceResourceUsage is observed consumption, used to size a limit correctly rather than by
	// multiplying the current one.
	EvidenceResourceUsage EvidenceKind = "ResourceUsage"
)

// CollectionStatus distinguishes evidence that was gathered, evidence that cannot exist for this
// failure, and evidence that should exist but could not be obtained. Collapsing the last two would
// make a confidence score uninterpretable, since it would hide what a conclusion was reached
// without.
// +kubebuilder:validation:Enum=Collected;NotApplicable;Unavailable
type CollectionStatus string

const (
	// CollectionCollected means the item was gathered.
	CollectionCollected CollectionStatus = "Collected"
	// CollectionNotApplicable means the item cannot exist for this failure. A container that never
	// started has no logs, and that absence is itself informative.
	CollectionNotApplicable CollectionStatus = "NotApplicable"
	// CollectionUnavailable means the item was expected but could not be obtained, for instance
	// resource usage in a cluster with no metrics server.
	CollectionUnavailable CollectionStatus = "Unavailable"
)

// EvidenceItem is one piece of gathered context, together with whether it was required and what
// happened when it was sought.
type EvidenceItem struct {
	Kind   EvidenceKind     `json:"kind"`
	Status CollectionStatus `json:"status"`

	// Required reports whether the requirement matrix considers this item necessary for the
	// failure type under diagnosis.
	// +optional
	Required bool `json:"required,omitempty"`

	// Reason explains a status of NotApplicable or Unavailable.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason,omitempty"`

	// Content is the gathered material, truncated to keep the object well inside API server size
	// limits. Content is captured rather than referenced because a failing pod may be deleted before
	// analysis runs, after which its logs are unrecoverable.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Content string `json:"content,omitempty"`
}

// Evidence is the diagnostic context gathered for a failure, collected against the requirement
// matrix for its failure type.
type Evidence struct {
	// Items is the outcome of every requirement for this failure type, whether or not it produced
	// content.
	// +optional
	// +listType=map
	// +listMapKey=kind
	Items []EvidenceItem `json:"items,omitempty"`

	// Complete reports that every required item was collected. When false, any conclusion drawn is
	// a best effort and its confidence is capped accordingly.
	//
	// This field is serialised even when false: an absent value and an explicit false would
	// otherwise be indistinguishable, which is exactly the ambiguity this stage exists to remove.
	// +optional
	Complete bool `json:"complete"`

	// MissingRequired names required items that could not be obtained.
	// +optional
	MissingRequired []EvidenceKind `json:"missingRequired,omitempty"`

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
	// This is the symptom the cluster displays, retained alongside the classified cause.
	// +optional
	WaitingReason string `json:"waitingReason,omitempty"`

	// RedactedFields counts values removed before storage. A non zero count means sensitive material
	// was present in the gathered content and did not leave the cluster.
	// +optional
	RedactedFields int32 `json:"redactedFields,omitempty"`
}

// Hypothesis is one candidate explanation for a failure. Investigations emit several, ranked, so
// that what was considered and rejected is visible rather than hidden behind a single score.
type Hypothesis struct {
	// ID identifies this hypothesis within the diagnosis, so a verdict and a plan can reference it.
	ID string `json:"id"`

	// Rank orders hypotheses, with 1 being the investigation's preferred explanation.
	// +kubebuilder:validation:Minimum=1
	Rank int32 `json:"rank"`

	// RootCause explains the failure in a form suitable for a pull request description.
	// +kubebuilder:validation:MaxLength=2048
	RootCause string `json:"rootCause"`

	// ConfidencePercent expresses certainty from 0 to 100. Integers are used throughout, since the
	// Kubernetes API conventions discourage floating point fields.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	ConfidencePercent int32 `json:"confidencePercent"`

	// EvidenceCitations names the specific observations supporting this hypothesis. A hypothesis
	// citing nothing cannot survive judgement, which is what keeps reasoning auditable.
	// +optional
	EvidenceCitations []string `json:"evidenceCitations,omitempty"`

	// ProposedActions are the remediations this explanation would imply, before ranking.
	// +optional
	ProposedActions []Action `json:"proposedActions,omitempty"`
}

// ToolCall records one read performed during an investigation. The sequence forms the audit trail
// of how a conclusion was reached.
type ToolCall struct {
	Tool string `json:"tool"`

	// +optional
	// +kubebuilder:validation:MaxLength=512
	Arguments string `json:"arguments,omitempty"`

	// Summary is a short description of what the call returned, not the full payload.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Summary string `json:"summary,omitempty"`

	// +optional
	DurationMillis int64 `json:"durationMillis,omitempty"`

	// +optional
	Error string `json:"error,omitempty"`
}

// Investigation records how an analysis was performed and what it consumed. It exists because an
// autonomous component acting on a cluster must be accountable for its own behaviour.
type Investigation struct {
	// Provider names the analyzer, such as the rule engine or a model backend.
	Provider string `json:"provider"`

	// Agentic reports whether this was a goal directed loop with tool selection, as opposed to a
	// deterministic evaluation or a single model call.
	Agentic bool `json:"agentic"`

	// ToolCalls is the ordered record of reads performed.
	// +optional
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`

	// +optional
	TokensUsed int32 `json:"tokensUsed,omitempty"`

	// CostMicroUSD is the spend in millionths of a dollar, stored as an integer to avoid floating
	// point fields.
	// +optional
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`

	// +optional
	DurationMillis int64 `json:"durationMillis,omitempty"`

	// BudgetExhausted reports that the loop stopped on a limit rather than on a conclusion, which
	// means the result is a best effort.
	// +optional
	BudgetExhausted bool `json:"budgetExhausted,omitempty"`
}

// Verdict is the judgement passed on a single hypothesis.
type Verdict struct {
	HypothesisID string `json:"hypothesisID"`

	// Supported reports whether the cited evidence actually establishes the claim.
	Supported bool `json:"supported"`

	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Reasoning string `json:"reasoning,omitempty"`

	// AdjustedConfidencePercent is the confidence after review, which may be lower than claimed.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	AdjustedConfidencePercent int32 `json:"adjustedConfidencePercent,omitempty"`
}

// Judgement is the review of an investigation's hypotheses against the evidence they cite. It runs
// separately from the investigation so that a failed review does not discard expensive work.
type Judgement struct {
	Provider string `json:"provider"`

	// +optional
	Verdicts []Verdict `json:"verdicts,omitempty"`

	// SelectedHypothesisID is the surviving hypothesis carried forward, empty when none survived.
	// +optional
	SelectedHypothesisID string `json:"selectedHypothesisID,omitempty"`
}

// RankedAction is a candidate remediation scored on the dimensions that determine the cost of being
// wrong. These are evaluated independently of diagnostic confidence.
type RankedAction struct {
	Action Action `json:"action"`

	BlastRadius   BlastRadius   `json:"blastRadius"`
	Reversibility Reversibility `json:"reversibility"`

	// AddressesRecentChange reports that this action undoes a change made shortly before the
	// failure began. Reverting a recent change is preferred over a novel forward fix.
	// +optional
	AddressesRecentChange bool `json:"addressesRecentChange,omitempty"`

	// PolicyEligible reports whether the governing policy permits this action type.
	PolicyEligible bool `json:"policyEligible"`

	// Score is the composite safety ranking, higher being safer and more preferred.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Score int32 `json:"score"`
}

// PlanDecision is the outcome of evaluating candidate actions against policy.
// +kubebuilder:validation:Enum=Apply;Defer;NoAction
type PlanDecision string

const (
	// PlanApply means a permitted action was selected and may be executed.
	PlanApply PlanDecision = "Apply"
	// PlanDefer means an action exists but policy requires a human, whether through dry run mode,
	// a confidence threshold, or an action type that is not permitted here.
	PlanDefer PlanDecision = "Defer"
	// PlanNoAction means no safe automated remedy exists for this cause.
	PlanNoAction PlanDecision = "NoAction"
)

// Plan is the selected remediation and the candidates it was chosen from.
type Plan struct {
	Decision PlanDecision `json:"decision"`

	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Reason string `json:"reason,omitempty"`

	// Selected is the action chosen for execution, absent when the decision is NoAction.
	// +optional
	Selected *Action `json:"selected,omitempty"`

	// Candidates records every action considered and how it scored, so a rejected option is visible
	// alongside the chosen one.
	// +optional
	Candidates []RankedAction `json:"candidates,omitempty"`
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

	// Investigation records how the analysis was performed and what it consumed.
	// +optional
	Investigation *Investigation `json:"investigation,omitempty"`

	// Hypotheses are the ranked candidate explanations produced by the investigation.
	// +optional
	Hypotheses []Hypothesis `json:"hypotheses,omitempty"`

	// Judgement is the review of those hypotheses against their cited evidence.
	// +optional
	Judgement *Judgement `json:"judgement,omitempty"`

	// Plan is the selected remediation and the candidates considered.
	// +optional
	Plan *Plan `json:"plan,omitempty"`

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
// +kubebuilder:printcolumn:name="Conf",type=integer,JSONPath=`.status.hypotheses[0].confidencePercent`
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.status.plan.selected.type`
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

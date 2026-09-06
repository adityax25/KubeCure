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

// FailureType is a pod failure mode that KubeCure can detect and reason about.
// Values map to conditions observable from container status, pod status, or scheduling events.
// +kubebuilder:validation:Enum=CrashLoopBackOff;OOMKilled;ImagePullBackOff;CreateContainerConfigError;RunContainerError;Unschedulable;ProbeFailure;Evicted
type FailureType string

const (
	// FailureCrashLoopBackOff indicates a container exiting non-zero and being restarted repeatedly.
	FailureCrashLoopBackOff FailureType = "CrashLoopBackOff"
	// FailureOOMKilled indicates the kernel terminated a container for exceeding its memory limit.
	FailureOOMKilled FailureType = "OOMKilled"
	// FailureImagePullBackOff indicates the container image could not be pulled.
	FailureImagePullBackOff FailureType = "ImagePullBackOff"
	// FailureCreateContainerConfigError indicates a referenced ConfigMap, Secret, or key is missing.
	FailureCreateContainerConfigError FailureType = "CreateContainerConfigError"
	// FailureRunContainerError indicates the runtime rejected the container configuration.
	FailureRunContainerError FailureType = "RunContainerError"
	// FailureUnschedulable indicates no node satisfies the pod's placement requirements.
	FailureUnschedulable FailureType = "Unschedulable"
	// FailureProbeFailure indicates a readiness or liveness probe is failing persistently.
	FailureProbeFailure FailureType = "ProbeFailure"
	// FailureEvicted indicates the pod was evicted, typically under node resource pressure.
	FailureEvicted FailureType = "Evicted"
)

// ActionType is a remediation the operator knows how to perform. The set is deliberately closed:
// an analyzer may only select from these, which bounds what any automated fix can change.
// +kubebuilder:validation:Enum=SetMemoryLimit;SetCPULimit;SetImage;SetProbeDelay;SetProbePort;AddResourceRequests;RollbackToRevision;RestartWorkload;None
type ActionType string

const (
	// ActionSetMemoryLimit adjusts a container's memory limit.
	ActionSetMemoryLimit ActionType = "SetMemoryLimit"
	// ActionSetCPULimit adjusts a container's CPU limit.
	ActionSetCPULimit ActionType = "SetCPULimit"
	// ActionSetImage replaces a container's image reference.
	ActionSetImage ActionType = "SetImage"
	// ActionSetProbeDelay adjusts a probe's initial delay.
	ActionSetProbeDelay ActionType = "SetProbeDelay"
	// ActionSetProbePort corrects the port a probe targets.
	ActionSetProbePort ActionType = "SetProbePort"
	// ActionAddResourceRequests adds resource requests where none are declared.
	ActionAddResourceRequests ActionType = "AddResourceRequests"
	// ActionRollbackToRevision returns a workload to an earlier revision. This is the one remedy
	// that addresses application level failures, since a regression introduced by a deployment can
	// be undone without changing source.
	ActionRollbackToRevision ActionType = "RollbackToRevision"
	// ActionRestartWorkload restarts a workload's pods, mitigating a stuck or leaking process while
	// a durable fix is prepared.
	ActionRestartWorkload ActionType = "RestartWorkload"
	// ActionNone indicates no safe automated fix exists, and the finding requires human judgement.
	ActionNone ActionType = "None"
)

// Action is a single proposed change, produced by an analyzer and executed by a remediator.
// It carries no manifest content: the remediator maps the type to a hand written handler, so the
// blast radius of any proposal is limited to the actions implemented here.
type Action struct {
	// Type selects which remediation handler applies.
	Type ActionType `json:"type"`

	// Container names the container within the pod spec that the action targets.
	// +optional
	Container string `json:"container,omitempty"`

	// Value is the action's parameter, interpreted per action type. Examples: "384Mi" for a memory
	// limit, "nginx:1.27.3" for an image, "60" for a probe delay in seconds.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Value string `json:"value,omitempty"`
}

// RemediationMode determines how an accepted action is applied.
// +kubebuilder:validation:Enum=DryRun;PullRequest;Direct
type RemediationMode string

const (
	// RemediationDryRun computes the change and records it without applying anything.
	RemediationDryRun RemediationMode = "DryRun"
	// RemediationPullRequest commits the change to a manifest repository and opens a pull request.
	RemediationPullRequest RemediationMode = "PullRequest"
	// RemediationDirect patches the live resource through the API server.
	RemediationDirect RemediationMode = "Direct"
)

// BlastRadius is how much of the cluster a remediation can affect if it is wrong. It is deliberately
// distinct from diagnostic confidence: confidence measures certainty about the cause, blast radius
// measures the cost of being mistaken.
// +kubebuilder:validation:Enum=Pod;Workload;Namespace;Cluster
type BlastRadius string

const (
	BlastRadiusPod       BlastRadius = "Pod"
	BlastRadiusWorkload  BlastRadius = "Workload"
	BlastRadiusNamespace BlastRadius = "Namespace"
	BlastRadiusCluster   BlastRadius = "Cluster"
)

// Reversibility is how quickly a remediation can be undone if it proves wrong.
// +kubebuilder:validation:Enum=Instant;Rollout;Irreversible
type Reversibility string

const (
	// ReversibilityInstant covers changes undone by writing the previous value back.
	ReversibilityInstant Reversibility = "Instant"
	// ReversibilityRollout covers changes requiring pods to be replaced before the effect is undone.
	ReversibilityRollout Reversibility = "Rollout"
	// ReversibilityIrreversible covers changes that cannot be undone, such as anything touching data.
	ReversibilityIrreversible Reversibility = "Irreversible"
)

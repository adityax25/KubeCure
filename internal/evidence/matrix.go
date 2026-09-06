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

package evidence

import (
	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

// Requirement states how a single evidence kind relates to a failure type.
type Requirement int

const (
	// NotApplicable means the item cannot exist for this failure, so its absence is informative
	// rather than a gap. A container that never started has no logs.
	NotApplicable Requirement = iota
	// Opportunistic means the item is useful when cheaply available but is not needed to conclude.
	Opportunistic
	// Required means an analysis should not conclude without the item while it remains obtainable.
	Required
)

// matrix declares, per failure type, what diagnosing it actually needs. Collection is driven by
// this table rather than by a fixed list, so an image pull failure is never asked for logs it
// cannot have, and a probe failure is never diagnosed without the Service that explains it.
//
// Entries omitted for a failure type default to NotApplicable.
//
// This table is intended to grow from measured misses: when the evaluation harness shows a wrong
// conclusion, the investigation trace identifies the evidence that would have resolved it, and that
// entry is promoted here (ADR-016).
var matrix = map[healingv1alpha1.FailureType]map[healingv1alpha1.EvidenceKind]Requirement{
	healingv1alpha1.FailureCrashLoopBackOff: {
		healingv1alpha1.EvidencePreviousLogs:   Required,
		healingv1alpha1.EvidenceEvents:         Required,
		healingv1alpha1.EvidenceWorkloadSpec:   Required,
		healingv1alpha1.EvidenceRolloutHistory: Required,
		healingv1alpha1.EvidenceCurrentLogs:    Opportunistic,
		healingv1alpha1.EvidenceSiblingPods:    Opportunistic,
	},
	healingv1alpha1.FailureOOMKilled: {
		healingv1alpha1.EvidencePreviousLogs:   Required,
		healingv1alpha1.EvidenceEvents:         Required,
		healingv1alpha1.EvidenceWorkloadSpec:   Required,
		healingv1alpha1.EvidenceRolloutHistory: Required,
		healingv1alpha1.EvidenceResourceUsage:  Required,
		healingv1alpha1.EvidenceCurrentLogs:    Opportunistic,
		healingv1alpha1.EvidenceSiblingPods:    Opportunistic,
	},
	healingv1alpha1.FailureImagePullBackOff: {
		healingv1alpha1.EvidenceEvents:         Required,
		healingv1alpha1.EvidenceWorkloadSpec:   Required,
		healingv1alpha1.EvidenceRolloutHistory: Opportunistic,
	},
	healingv1alpha1.FailureCreateContainerConfigError: {
		healingv1alpha1.EvidenceEvents:           Required,
		healingv1alpha1.EvidenceWorkloadSpec:     Required,
		healingv1alpha1.EvidenceConfigReferences: Required,
		healingv1alpha1.EvidenceRolloutHistory:   Opportunistic,
	},
	healingv1alpha1.FailureRunContainerError: {
		healingv1alpha1.EvidenceEvents:           Required,
		healingv1alpha1.EvidenceWorkloadSpec:     Required,
		healingv1alpha1.EvidenceConfigReferences: Opportunistic,
	},
	healingv1alpha1.FailureUnschedulable: {
		healingv1alpha1.EvidenceEvents:       Required,
		healingv1alpha1.EvidenceWorkloadSpec: Required,
		healingv1alpha1.EvidenceNodeStatus:   Required,
		healingv1alpha1.EvidenceSiblingPods:  Opportunistic,
	},
	healingv1alpha1.FailureProbeFailure: {
		healingv1alpha1.EvidenceCurrentLogs:      Required,
		healingv1alpha1.EvidenceEvents:           Required,
		healingv1alpha1.EvidenceWorkloadSpec:     Required,
		healingv1alpha1.EvidenceServiceEndpoints: Required,
		healingv1alpha1.EvidencePreviousLogs:     Opportunistic,
		healingv1alpha1.EvidenceRolloutHistory:   Opportunistic,
	},
	healingv1alpha1.FailureEvicted: {
		healingv1alpha1.EvidenceEvents:        Required,
		healingv1alpha1.EvidenceWorkloadSpec:  Required,
		healingv1alpha1.EvidenceNodeStatus:    Required,
		healingv1alpha1.EvidenceSiblingPods:   Required,
		healingv1alpha1.EvidencePreviousLogs:  Opportunistic,
		healingv1alpha1.EvidenceResourceUsage: Opportunistic,
	},
}

// allKinds is the full catalogue, used so that every kind is reported for every failure, including
// those that are not applicable. Reporting the full set is what distinguishes "cannot exist" from
// "was not attempted".
var allKinds = []healingv1alpha1.EvidenceKind{
	healingv1alpha1.EvidencePreviousLogs,
	healingv1alpha1.EvidenceCurrentLogs,
	healingv1alpha1.EvidenceEvents,
	healingv1alpha1.EvidenceWorkloadSpec,
	healingv1alpha1.EvidenceRolloutHistory,
	healingv1alpha1.EvidenceConfigReferences,
	healingv1alpha1.EvidenceServiceEndpoints,
	healingv1alpha1.EvidenceNodeStatus,
	healingv1alpha1.EvidenceSiblingPods,
	healingv1alpha1.EvidenceResourceUsage,
}

// RequirementFor reports how an evidence kind relates to a failure type.
func RequirementFor(failureType healingv1alpha1.FailureType, kind healingv1alpha1.EvidenceKind) Requirement {
	if kinds, ok := matrix[failureType]; ok {
		if req, ok := kinds[kind]; ok {
			return req
		}
	}
	return NotApplicable
}

// Plan returns the evidence kinds worth attempting for a failure type, required ones first so that
// a collection interrupted by a deadline still satisfies the requirements it can.
func Plan(failureType healingv1alpha1.FailureType) []healingv1alpha1.EvidenceKind {
	var required, opportunistic []healingv1alpha1.EvidenceKind
	for _, kind := range allKinds {
		switch RequirementFor(failureType, kind) {
		case Required:
			required = append(required, kind)
		case Opportunistic:
			opportunistic = append(opportunistic, kind)
		}
	}
	return append(required, opportunistic...)
}

// NotApplicableReason explains why a kind cannot exist for a failure type, so that its absence is
// recorded as an observation rather than as a gap.
func NotApplicableReason(failureType healingv1alpha1.FailureType, kind healingv1alpha1.EvidenceKind) string {
	switch kind {
	case healingv1alpha1.EvidencePreviousLogs, healingv1alpha1.EvidenceCurrentLogs:
		switch failureType {
		case healingv1alpha1.FailureImagePullBackOff:
			return "no image was pulled, so no container ever ran"
		case healingv1alpha1.FailureCreateContainerConfigError, healingv1alpha1.FailureRunContainerError:
			return "the container could not be created, so it produced no output"
		case healingv1alpha1.FailureUnschedulable:
			return "the pod was never placed on a node"
		}
	case healingv1alpha1.EvidenceServiceEndpoints:
		return "not relevant to this failure type"
	case healingv1alpha1.EvidenceNodeStatus:
		return "the failure is not attributable to the host node"
	}
	return "not relevant to this failure type"
}

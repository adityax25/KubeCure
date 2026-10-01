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

package controller

import (
	"encoding/json"
	"strings"
	"testing"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/agent"
)

func oomDiagnosis() *healingv1alpha1.Diagnosis {
	exit := int32(137)
	return &healingv1alpha1.Diagnosis{
		Spec: healingv1alpha1.DiagnosisSpec{
			FailureType: healingv1alpha1.FailureOOMKilled,
			Container:   "api",
			Target: healingv1alpha1.TargetRef{
				Name:  "oomkilled-api-x1",
				Owner: &healingv1alpha1.OwnerRef{Kind: "Deployment", Name: "oomkilled-api"},
			},
		},
		Status: healingv1alpha1.DiagnosisStatus{Evidence: &healingv1alpha1.Evidence{
			ExitCode:          &exit,
			TerminationReason: "OOMKilled",
			WaitingReason:     "CrashLoopBackOff",
			Items: []healingv1alpha1.EvidenceItem{
				{Kind: healingv1alpha1.EvidencePreviousLogs, Status: healingv1alpha1.CollectionCollected, Required: true, Content: strings.Repeat("x", 4000)},
				{Kind: healingv1alpha1.EvidenceRolloutHistory, Status: healingv1alpha1.CollectionCollected, Required: true, Content: "rev 1"},
				{Kind: healingv1alpha1.EvidenceResourceUsage, Status: healingv1alpha1.CollectionUnavailable, Required: true, Reason: "no metrics server"},
				{Kind: healingv1alpha1.EvidenceServiceEndpoints, Status: healingv1alpha1.CollectionNotApplicable},
			},
		}},
	}
}

func TestBuildInputMapsEvidenceToRequirements(t *testing.T) {
	in := buildInput(oomDiagnosis(), &healingv1alpha1.AgentConfig{MaxToolCalls: 8}, agent.Pricing{})

	if in.Workload != "Deployment/oomkilled-api" {
		t.Errorf("expected the workload, not the pod, got %q", in.Workload)
	}
	if !strings.Contains(strings.Join(in.Facts, "|"), "Currently displayed as: CrashLoopBackOff") {
		t.Errorf("expected the displayed symptom among the facts, got %v", in.Facts)
	}

	if len(in.Baseline) != 2 || in.Baseline[0].ID != "B:PreviousLogs" {
		t.Fatalf("expected only collected items in the baseline, got %+v", in.Baseline)
	}
	if len(in.Baseline[0].Excerpt) > baselineExcerptLimit+32 {
		t.Errorf("expected the baseline excerpt to be bounded, got %d bytes", len(in.Baseline[0].Excerpt))
	}

	usage := in.Requirements[2]
	if usage.Kind != "ResourceUsage" || usage.Collected || usage.Obtainable || usage.Tool != "get_resource_usage" {
		t.Fatalf("expected unavailable usage to be unobtainable, got %+v", usage)
	}
	if len(in.Requirements) != 3 {
		t.Errorf("expected only required items as requirements, got %d", len(in.Requirements))
	}
}

func TestLogRequirementsDistinguishPreviousFromCurrent(t *testing.T) {
	previous := satisfierFor(healingv1alpha1.EvidencePreviousLogs)
	current := satisfierFor(healingv1alpha1.EvidenceCurrentLogs)

	if !previous("get_pod_logs", json.RawMessage(`{"previous":true}`)) {
		t.Error("previous logs should be satisfied by a call with previous=true")
	}
	if previous("get_pod_logs", json.RawMessage(`{}`)) {
		t.Error("previous logs must not be satisfied by reading the running instance")
	}
	if !current("get_pod_logs", json.RawMessage(`{}`)) {
		t.Error("current logs should be satisfied by a call without previous")
	}
}

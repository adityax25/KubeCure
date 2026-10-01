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

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/agent"
)

// similarLimit bounds how many past diagnoses a history search returns.
const similarLimit = 10

// Argument schemas. Every tool declares an object schema with additionalProperties disabled, which
// mirrors the strict decoding applied to the arguments themselves.
const (
	schemaNone = `{"type":"object","properties":{},"additionalProperties":false}`

	schemaPod = `{"type":"object","properties":{
		"pod":{"type":"string","description":"Pod name. Omit to use the diagnosed pod, or a live pod of the same workload if it has been replaced."}
	},"additionalProperties":false}`

	schemaLogs = `{"type":"object","properties":{
		"pod":{"type":"string","description":"Pod name. Omit to use the diagnosed pod."},
		"container":{"type":"string","description":"Container name. Omit to use the failing container."},
		"previous":{"type":"boolean","description":"Read the instance that terminated rather than the running one. A restarting container records its fatal error only in the previous instance."}
	},"additionalProperties":false}`

	schemaSimilar = `{"type":"object","properties":{
		"failureType":{"type":"string","description":"Failure type to match. Omit to use the type under investigation."}
	},"additionalProperties":false}`
)

type podArgs struct {
	Pod string `json:"pod"`
}

type logArgs struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Previous  bool   `json:"previous"`
}

type similarArgs struct {
	FailureType string `json:"failureType"`
}

// catalog defines every tool. None of them mutates anything: the catalogue is read only by
// construction, so there is no write capability to acquire (ADR-017).
func catalog() []tool {
	return []tool{
		{
			spec: spec("get_pod_logs",
				"Read container output. Use previous=true for a container that has restarted, since its fatal error is recorded only by the instance that died.",
				schemaLogs),
			run: runLogs,
		},
		{
			spec: spec("get_pod_events",
				"Read a pod's events, most recent first. Scheduler verdicts, image pull errors, and missing ConfigMap names appear here.",
				schemaPod),
			run: podEvidence(healingv1alpha1.EvidenceEvents),
		},
		{
			spec: spec("get_workload_spec",
				"Read the owning workload's pod template: images, commands, resource requests and limits, probes, and ports.",
				schemaNone),
			run: workloadEvidence(healingv1alpha1.EvidenceWorkloadSpec),
		},
		{
			spec: spec("get_recent_changes",
				"Read the workload's recent revisions with their creation times and container specs. Use this to establish whether a change shortly preceded the failure.",
				schemaNone),
			run: workloadEvidence(healingv1alpha1.EvidenceRolloutHistory),
		},
		{
			spec: spec("get_related_pods",
				"Read the state of every pod in the same workload, to distinguish one unhealthy pod from a workload wide fault.",
				schemaNone),
			run: podEvidence(healingv1alpha1.EvidenceSiblingPods),
		},
		{
			spec: spec("get_service_endpoints",
				"Read the Services selecting a pod and whether that pod is currently serving traffic through them.",
				schemaPod),
			run: podEvidence(healingv1alpha1.EvidenceServiceEndpoints),
		},
		{
			spec: spec("get_node_status",
				"Read the host node's conditions, taints, and allocatable capacity. For an unscheduled pod, reads every node.",
				schemaPod),
			run: podEvidence(healingv1alpha1.EvidenceNodeStatus),
		},
		{
			spec: spec("get_config_references",
				"Report whether every ConfigMap and Secret a pod references exists, with their key names. Values are never read.",
				schemaPod),
			run: podEvidence(healingv1alpha1.EvidenceConfigReferences),
		},
		{
			spec: spec("get_resource_usage",
				"Read a pod's observed CPU and memory consumption. Reports when no metrics server is installed.",
				schemaPod),
			run: podEvidence(healingv1alpha1.EvidenceResourceUsage),
		},
		{
			spec: spec("search_similar_diagnoses",
				"Find earlier diagnoses in this namespace with the same failure type, with their conclusions and outcomes.",
				schemaSimilar),
			run: runSimilar,
		},
	}
}

func spec(name, description, schema string) agent.ToolSpec {
	return agent.ToolSpec{Name: name, Description: description, InputSchema: json.RawMessage(schema)}
}

func runLogs(ctx context.Context, b *Broker, arguments json.RawMessage) (string, error) {
	var args logArgs
	if err := decodeArgs(arguments, &args); err != nil {
		return "", err
	}
	pod, err := b.resolvePod(ctx, args.Pod)
	if err != nil {
		return "", err
	}
	target := b.target(pod, args.Container)
	if target.Container != "" && !hasContainer(pod, target.Container) {
		return "", denied("pod %q has no container %q", pod.Name, target.Container)
	}

	kind := healingv1alpha1.EvidenceCurrentLogs
	if args.Previous {
		kind = healingv1alpha1.EvidencePreviousLogs
	}
	return b.registry.collector.Gather(ctx, target, kind)
}

// podEvidence builds a handler for reads that concern one pod in the workload.
func podEvidence(kind healingv1alpha1.EvidenceKind) handler {
	return func(ctx context.Context, b *Broker, arguments json.RawMessage) (string, error) {
		var args podArgs
		if err := decodeArgs(arguments, &args); err != nil {
			return "", err
		}
		pod, err := b.resolvePod(ctx, args.Pod)
		if err != nil {
			return "", err
		}
		return b.registry.collector.Gather(ctx, b.target(pod, ""), kind)
	}
}

// workloadEvidence builds a handler for reads that concern the workload rather than any one pod.
func workloadEvidence(kind healingv1alpha1.EvidenceKind) handler {
	return func(ctx context.Context, b *Broker, arguments json.RawMessage) (string, error) {
		var args struct{}
		if err := decodeArgs(arguments, &args); err != nil {
			return "", err
		}
		return b.registry.collector.Gather(ctx, b.workloadTarget(), kind)
	}
}

// runSimilar searches this namespace's diagnosis history. It is confined to the bound namespace and
// excludes the diagnosis under investigation.
func runSimilar(ctx context.Context, b *Broker, arguments json.RawMessage) (string, error) {
	var args similarArgs
	if err := decodeArgs(arguments, &args); err != nil {
		return "", err
	}
	failureType := healingv1alpha1.FailureType(args.FailureType)
	if failureType == "" {
		failureType = b.scope.FailureType
	}

	list := &healingv1alpha1.DiagnosisList{}
	if err := b.registry.client.List(ctx, list, client.InNamespace(b.scope.Namespace)); err != nil {
		return "", err
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].CreationTimestamp.After(list.Items[j].CreationTimestamp.Time)
	})

	var found []map[string]any
	for i := range list.Items {
		d := &list.Items[i]
		if d.Name == b.scope.DiagnosisName || d.Spec.FailureType != failureType {
			continue
		}
		entry := map[string]any{
			"diagnosis": d.Name,
			"created":   d.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
			"phase":     d.Status.Phase,
		}
		if owner := d.Spec.Target.Owner; owner != nil {
			entry["workload"] = owner.Kind + "/" + owner.Name
		}
		if len(d.Status.Hypotheses) > 0 {
			entry["rootCause"] = d.Status.Hypotheses[0].RootCause
		}
		if d.Status.Plan != nil && d.Status.Plan.Selected != nil {
			entry["action"] = d.Status.Plan.Selected.Type
		}
		if d.Status.Remediation != nil && d.Status.Remediation.Verified != nil {
			entry["verified"] = *d.Status.Remediation.Verified
		}
		found = append(found, entry)
		if len(found) >= similarLimit {
			break
		}
	}

	if len(found) == 0 {
		return fmt.Sprintf("no earlier %s diagnoses in this namespace", failureType), nil
	}
	data, err := json.MarshalIndent(found, "", "  ")
	return string(data), err
}

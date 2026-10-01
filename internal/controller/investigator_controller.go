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
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/agent"
	"github.com/adityax25/KubeCure/internal/tools"
)

const (
	// baselineExcerptLimit bounds how much of each baseline item opens the conversation. The full
	// content stays on the Diagnosis and remains reachable through the corresponding tool; the
	// excerpt only has to orient the model, and every turn resends it.
	baselineExcerptLimit = 1500

	// brokerHeadroom lets the broker's own ceiling sit just above the agent's tool budget, so it
	// acts as a backstop rather than the primary limit.
	brokerHeadroom = 2

	// investigatorConcurrency is deliberately low: each investigation holds a model call open for
	// seconds and spends against a rate limited, paid API.
	investigatorConcurrency = 2

	// maxRecordedToolCalls bounds the audit trail stored on the object.
	maxRecordedToolCalls = 30
)

// toolForEvidence maps each evidence kind to the tool that gathers it, so the sufficiency gate can
// tell the model exactly what to call for anything still missing.
var toolForEvidence = map[healingv1alpha1.EvidenceKind]string{
	healingv1alpha1.EvidencePreviousLogs:     "get_pod_logs",
	healingv1alpha1.EvidenceCurrentLogs:      "get_pod_logs",
	healingv1alpha1.EvidenceEvents:           "get_pod_events",
	healingv1alpha1.EvidenceWorkloadSpec:     "get_workload_spec",
	healingv1alpha1.EvidenceRolloutHistory:   "get_recent_changes",
	healingv1alpha1.EvidenceConfigReferences: "get_config_references",
	healingv1alpha1.EvidenceServiceEndpoints: "get_service_endpoints",
	healingv1alpha1.EvidenceNodeStatus:       "get_node_status",
	healingv1alpha1.EvidenceSiblingPods:      "get_related_pods",
	healingv1alpha1.EvidenceResourceUsage:    "get_resource_usage",
}

// proposableActions is every action type the model may propose. Whether an action is permitted in
// a namespace is decided later by the planner against policy; the investigator only bounds what
// can be expressed at all.
var proposableActions = []string{
	string(healingv1alpha1.ActionSetMemoryLimit),
	string(healingv1alpha1.ActionSetCPULimit),
	string(healingv1alpha1.ActionSetImage),
	string(healingv1alpha1.ActionSetProbeDelay),
	string(healingv1alpha1.ActionSetProbePort),
	string(healingv1alpha1.ActionAddResourceRequests),
	string(healingv1alpha1.ActionRollbackToRevision),
	string(healingv1alpha1.ActionRestartWorkload),
	string(healingv1alpha1.ActionNone),
}

// InvestigatorReconciler runs the goal directed investigation for diagnoses whose evidence has been
// gathered. It is the only agentic stage in the pipeline.
//
// Its invariant: every Enriched diagnosis in a namespace whose policy enables the agent has been
// investigated, and carries ranked hypotheses or an explicit record of why it has none.
type InvestigatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Registry builds the scoped broker each investigation reads through.
	Registry *tools.Registry

	// Model performs the reasoning. Nil when no model is configured, in which case diagnoses wait
	// at Enriched for the rule engine rather than being given a fabricated analysis.
	Model agent.Model

	// Pricing converts token usage into cost for the cost budget.
	Pricing agent.Pricing
}

// +kubebuilder:rbac:groups=healing.kubecure.io,resources=healingpolicies,verbs=get;list;watch

func (r *InvestigatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	diagnosis := &healingv1alpha1.Diagnosis{}
	if err := r.Get(ctx, req.NamespacedName, diagnosis); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if diagnosis.Status.Phase != healingv1alpha1.PhaseEnriched {
		return ctrl.Result{}, nil
	}

	policy := &healingv1alpha1.HealingPolicy{}
	key := types.NamespacedName{Namespace: diagnosis.Namespace, Name: diagnosis.Spec.PolicyRef}
	if err := r.Get(ctx, key, policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !policy.Spec.Agent.Enabled {
		return ctrl.Result{}, nil
	}
	if r.Model == nil {
		log.Info("agent enabled by policy but no model is configured; leaving diagnosis at Enriched",
			"diagnosis", diagnosis.Name)
		return ctrl.Result{}, nil
	}

	in := buildInput(diagnosis, &policy.Spec.Agent, r.Pricing)
	scope := tools.ScopeForDiagnosis(diagnosis, in.Budget.MaxToolCalls+brokerHeadroom)
	broker := r.Registry.BindTo(scope)

	findings, err := agent.NewInvestigator(r.Model, broker).Run(ctx, in)
	if err != nil {
		// A provider failure says nothing about the workload, so it is retried with backoff.
		return ctrl.Result{}, fmt.Errorf("investigating %s: %w", diagnosis.Name, err)
	}

	if err := r.record(ctx, req.NamespacedName, findings, broker.Calls()); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("investigation complete",
		"diagnosis", diagnosis.Name,
		"stopReason", findings.StopReason,
		"hypotheses", len(findings.Hypotheses),
		"toolCalls", findings.ToolCalls,
		"turns", findings.Turns,
		"tokens", findings.Tokens,
		"degraded", findings.Degraded,
	)
	return ctrl.Result{}, nil
}

// record writes the findings onto the latest version of the object. The investigation is the
// expensive part, so a write conflict is retried against a fresh read rather than by re running it.
func (r *InvestigatorReconciler) record(ctx context.Context, key types.NamespacedName, f agent.Findings, calls []healingv1alpha1.ToolCall) error {
	if len(calls) > maxRecordedToolCalls {
		calls = calls[:maxRecordedToolCalls]
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &healingv1alpha1.Diagnosis{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		if latest.Status.Phase != healingv1alpha1.PhaseEnriched {
			return nil
		}

		latest.Status.Investigation = &healingv1alpha1.Investigation{
			Provider:        r.Model.Name(),
			Agentic:         true,
			ToolCalls:       calls,
			TokensUsed:      int32(f.Tokens),
			CostMicroUSD:    f.CostMicroUSD,
			DurationMillis:  f.Duration.Milliseconds(),
			BudgetExhausted: f.BudgetExhausted,
			StopReason:      f.StopReason,
			Degraded:        f.Degraded,
			Turns:           int32(f.Turns),
		}
		latest.Status.Hypotheses = toAPIHypotheses(f.Hypotheses)
		latest.Status.Phase = healingv1alpha1.PhaseHypothesized
		now := metav1.Now()
		latest.Status.Timings.HypothesizedAt = &now
		return r.Status().Update(ctx, latest)
	})
}

// buildInput assembles what the investigation starts from: facts already known, a short excerpt of
// each baseline item with the identifier it is cited by, and the requirement state that drives the
// sufficiency gate.
func buildInput(d *healingv1alpha1.Diagnosis, cfg *healingv1alpha1.AgentConfig, pricing agent.Pricing) agent.Input {
	in := agent.Input{
		FailureType:    string(d.Spec.FailureType),
		Namespace:      d.Namespace,
		Workload:       "Pod/" + d.Spec.Target.Name,
		Container:      d.Spec.Container,
		AllowedActions: proposableActions,
		Pricing:        pricing,
		Budget: agent.Budget{
			MaxToolCalls:    int(cfg.MaxToolCalls),
			MaxTokens:       int(cfg.MaxTokens),
			MaxCostMicroUSD: cfg.MaxCostMicroUSD,
			Timeout:         time.Duration(cfg.TimeoutSeconds) * time.Second,
		},
	}
	if owner := d.Spec.Target.Owner; owner != nil {
		in.Workload = owner.Kind + "/" + owner.Name
	}

	ev := d.Status.Evidence
	if ev == nil {
		return in
	}

	if ev.ExitCode != nil {
		in.Facts = append(in.Facts, fmt.Sprintf("Previous exit code: %d", *ev.ExitCode))
	}
	if ev.TerminationReason != "" {
		in.Facts = append(in.Facts, "Previous termination reason: "+ev.TerminationReason)
	}
	if ev.WaitingReason != "" {
		in.Facts = append(in.Facts, "Currently displayed as: "+ev.WaitingReason)
	}
	in.Facts = append(in.Facts, fmt.Sprintf("Restart count: %d", ev.RestartCount))

	for _, item := range ev.Items {
		if item.Status == healingv1alpha1.CollectionCollected {
			in.Baseline = append(in.Baseline, agent.BaselineItem{
				ID:      "B:" + string(item.Kind),
				Kind:    string(item.Kind),
				Excerpt: excerpt(item.Content, baselineExcerptLimit),
			})
		}
		if !item.Required {
			continue
		}
		in.Requirements = append(in.Requirements, agent.Requirement{
			Kind:        string(item.Kind),
			Tool:        toolForEvidence[item.Kind],
			Collected:   item.Status == healingv1alpha1.CollectionCollected,
			Obtainable:  item.Status != healingv1alpha1.CollectionUnavailable,
			SatisfiedBy: satisfierFor(item.Kind),
		})
	}
	return in
}

// satisfierFor distinguishes the two log kinds, which share one tool and differ by an argument.
func satisfierFor(kind healingv1alpha1.EvidenceKind) func(string, json.RawMessage) bool {
	tool := toolForEvidence[kind]
	switch kind {
	case healingv1alpha1.EvidencePreviousLogs, healingv1alpha1.EvidenceCurrentLogs:
		wantPrevious := kind == healingv1alpha1.EvidencePreviousLogs
		return func(name string, arguments json.RawMessage) bool {
			if name != tool {
				return false
			}
			var args struct {
				Previous bool `json:"previous"`
			}
			_ = json.Unmarshal(arguments, &args)
			return args.Previous == wantPrevious
		}
	default:
		return func(name string, _ json.RawMessage) bool { return name == tool }
	}
}

func excerpt(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	return content[:limit] + "\n[excerpt truncated]"
}

func toAPIHypotheses(in []agent.Hypothesis) []healingv1alpha1.Hypothesis {
	out := make([]healingv1alpha1.Hypothesis, 0, len(in))
	for _, h := range in {
		actions := make([]healingv1alpha1.Action, 0, len(h.ProposedActions))
		for _, a := range h.ProposedActions {
			actions = append(actions, healingv1alpha1.Action{
				Type:      healingv1alpha1.ActionType(a.Type),
				Container: a.Container,
				Value:     a.Value,
			})
		}
		out = append(out, healingv1alpha1.Hypothesis{
			ID:                h.ID,
			Rank:              int32(h.Rank),
			RootCause:         h.RootCause,
			ConfidencePercent: int32(h.ConfidencePercent),
			EvidenceCitations: h.EvidenceCitations,
			ProposedActions:   actions,
		})
	}
	return out
}

func (r *InvestigatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&healingv1alpha1.Diagnosis{}, builder.WithPredicates(phaseIs(healingv1alpha1.PhaseEnriched))).
		WithOptions(crcontroller.Options{MaxConcurrentReconciles: investigatorConcurrency}).
		Named("investigator").
		Complete(r)
}

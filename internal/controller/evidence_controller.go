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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/evidence"
)

// EvidenceReconciler gathers diagnostic context for diagnoses awaiting it.
//
// Its invariant: every diagnosis at Detected has evidence attached. Collection is driven by the
// requirement matrix for the failure type rather than by a fixed list, so an image pull failure is
// never asked for logs it cannot have, and a probe failure is never diagnosed without the Service
// that explains it (ADR-016).
type EvidenceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Clientset serves container logs, which are a subresource the shared cache cannot provide.
	Clientset kubernetes.Interface
}

// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=configmaps;secrets;services;endpoints;nodes,verbs=get;list;watch

func (r *EvidenceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	diagnosis := &healingv1alpha1.Diagnosis{}
	if err := r.Get(ctx, req.NamespacedName, diagnosis); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if diagnosis.Status.Phase != healingv1alpha1.PhaseDetected {
		return ctrl.Result{}, nil
	}

	target, err := r.targetFor(ctx, diagnosis)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The pod is gone. Its logs are unrecoverable, so the diagnosis is advanced with whatever
			// could be established rather than retried indefinitely against an object that no longer
			// exists.
			return r.advanceWithoutPod(ctx, diagnosis)
		}
		return ctrl.Result{}, err
	}

	collector := &evidence.Collector{Client: r.Client, Clientset: r.Clientset}
	collected := collector.Collect(ctx, target)

	diagnosis.Status.Evidence = collected
	diagnosis.Status.Phase = healingv1alpha1.PhaseEnriched
	now := metav1.Now()
	diagnosis.Status.Timings.EnrichedAt = &now

	if err := r.Status().Update(ctx, diagnosis); err != nil {
		return ctrl.Result{}, err
	}

	fields := []any{
		"diagnosis", diagnosis.Name,
		"failureType", diagnosis.Spec.FailureType,
		"collected", countCollected(collected),
		"complete", collected.Complete,
	}
	if collected.RedactedFields > 0 {
		fields = append(fields, "redacted", collected.RedactedFields)
	}
	if len(collected.MissingRequired) > 0 {
		fields = append(fields, "missingRequired", collected.MissingRequired)
	}
	log.Info("evidence gathered", fields...)

	return ctrl.Result{}, nil
}

func (r *EvidenceReconciler) targetFor(ctx context.Context, diagnosis *healingv1alpha1.Diagnosis) (evidence.Target, error) {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Namespace: diagnosis.Namespace, Name: diagnosis.Spec.Target.Name}
	if err := r.Get(ctx, key, pod); err != nil {
		return evidence.Target{}, err
	}

	// A pod name can be reused after the original is deleted. The recorded UID is what guarantees
	// the evidence describes the instance that actually failed.
	if pod.UID != diagnosis.Spec.Target.UID {
		return evidence.Target{}, apierrors.NewNotFound(
			corev1.Resource("pods"), fmt.Sprintf("%s (UID mismatch)", pod.Name))
	}

	target := evidence.Target{
		Pod:         pod,
		Container:   diagnosis.Spec.Container,
		FailureType: diagnosis.Spec.FailureType,
		OwnerKind:   "Pod",
		OwnerName:   pod.Name,
	}
	if owner := diagnosis.Spec.Target.Owner; owner != nil {
		target.OwnerKind = owner.Kind
		target.OwnerName = owner.Name
	}
	return target, nil
}

// advanceWithoutPod records that the subject disappeared before evidence could be gathered. The
// diagnosis still progresses, carrying an explicit account of why it is incomplete.
func (r *EvidenceReconciler) advanceWithoutPod(ctx context.Context, diagnosis *healingv1alpha1.Diagnosis) (ctrl.Result, error) {
	diagnosis.Status.Evidence = &healingv1alpha1.Evidence{
		Complete: false,
		Items: []healingv1alpha1.EvidenceItem{{
			Kind:     healingv1alpha1.EvidencePreviousLogs,
			Status:   healingv1alpha1.CollectionUnavailable,
			Required: true,
			Reason:   "the pod was deleted before evidence could be gathered",
		}},
		MissingRequired: []healingv1alpha1.EvidenceKind{healingv1alpha1.EvidencePreviousLogs},
	}
	diagnosis.Status.Phase = healingv1alpha1.PhaseEnriched
	now := metav1.Now()
	diagnosis.Status.Timings.EnrichedAt = &now
	return ctrl.Result{}, r.Status().Update(ctx, diagnosis)
}

func countCollected(ev *healingv1alpha1.Evidence) int {
	n := 0
	for i := range ev.Items {
		if ev.Items[i].Status == healingv1alpha1.CollectionCollected {
			n++
		}
	}
	return n
}

func (r *EvidenceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&healingv1alpha1.Diagnosis{}, builder.WithPredicates(phaseIs(healingv1alpha1.PhaseDetected))).
		Named("evidence").
		Complete(r)
}

// phaseIs filters diagnoses down to a single phase, so each controller in the pipeline wakes only
// for work that belongs to it rather than for every status write by every other stage.
func phaseIs(phase healingv1alpha1.Phase) predicate.Predicate {
	matches := func(obj client.Object) bool {
		diagnosis, ok := obj.(*healingv1alpha1.Diagnosis)
		return ok && diagnosis.Status.Phase == phase
	}
	return predicate.NewPredicateFuncs(matches)
}

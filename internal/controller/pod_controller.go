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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/detector"
)

// defaultPolicyName is the conventional name for a namespace's governing policy. When several
// policies exist, this one is preferred.
const defaultPolicyName = "default"

// systemNamespaces are excluded from detection. Control plane components failing are the cluster
// operator's concern, not an application diagnosis, and reporting them would produce noise the
// system cannot act on.
var systemNamespaces = map[string]struct{}{
	"kube-system":        {},
	"kube-public":        {},
	"kube-node-lease":    {},
	"local-path-storage": {},
	"kubecure-system":    {},
}

// PodReconciler is the detector. It watches pods, classifies failures, and opens one Diagnosis per
// distinct failure signature.
//
// Its invariant is narrow: every failing pod in an opted in namespace has a corresponding
// Diagnosis. It does not attempt to repair anything, and it never mutates a workload.
type PodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=healing.kubecure.io,resources=diagnoses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=healing.kubecure.io,resources=diagnoses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=healing.kubecure.io,resources=healingpolicies,verbs=get;list;watch

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if _, excluded := systemNamespaces[req.Namespace]; excluded {
		return ctrl.Result{}, nil
	}

	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	signal := detector.Classify(pod, time.Now())
	if signal == nil {
		return ctrl.Result{}, nil
	}

	// A namespace with no policy is not opted in. The failure is left alone rather than recorded,
	// so that installing the operator changes nothing until a team asks for it (ADR-012).
	policy, err := r.policyFor(ctx, pod.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if policy == nil {
		return ctrl.Result{}, nil
	}

	if !policyCovers(policy, signal.FailureType) {
		return ctrl.Result{}, nil
	}

	workload, err := r.resolveWorkload(ctx, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving owning workload: %w", err)
	}

	signature := detector.Signature(pod.Namespace, workload.Kind, workload.Name, signal.Container, signal.FailureType)
	name := detector.DiagnosisName(workload.Name, signal.FailureType, signature)

	// A deterministic name makes the API server the deduplication mechanism: a second create for the
	// same signature is rejected as already existing, so replicas failing identically collapse into
	// one diagnosis without any local cache (ADR-008).
	existing := &healingv1alpha1.Diagnosis{}
	err = r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: name}, existing)
	if err == nil {
		return ctrl.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	within, err := r.withinRateLimit(ctx, pod.Namespace, policy)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !within {
		log.Info("diagnosis rate limit reached, skipping",
			"namespace", pod.Namespace, "limit", policy.Spec.MaxDiagnosesPerHour)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	diagnosis := r.buildDiagnosis(pod, workload, signal, policy, name, signature)
	if err := r.Create(ctx, diagnosis); err != nil {
		// Losing a create race is the expected outcome when several replicas fail together.
		return ctrl.Result{}, client.IgnoreAlreadyExists(err)
	}

	diagnosis.Status.Phase = healingv1alpha1.PhaseDetected
	now := metav1.Now()
	diagnosis.Status.Timings.DetectedAt = &now
	if err := r.Status().Update(ctx, diagnosis); err != nil {
		return ctrl.Result{}, err
	}

	fields := []any{
		"diagnosis", name,
		"failureType", signal.FailureType,
		"workload", workload.Kind + "/" + workload.Name,
	}
	// The displayed reason is only worth recording when it differs from the classified cause, which
	// is the case this system exists to handle.
	if signal.ObservedReason != "" && signal.ObservedReason != signal.Reason {
		fields = append(fields, "displayedAs", signal.ObservedReason)
	}
	log.Info("opened diagnosis", fields...)

	return ctrl.Result{}, nil
}

// workloadRef is the top level object a failure is attributed to, and the object a fix is applied
// to. It is not the pod's direct owner: a pod managed by a Deployment is owned by a ReplicaSet whose
// name changes on every rollout, so attributing to it would open a fresh diagnosis after each
// deploy for a bug that never went away.
type workloadRef struct {
	Kind string
	Name string
	UID  types.UID
}

func (r *PodReconciler) resolveWorkload(ctx context.Context, pod *corev1.Pod) (workloadRef, error) {
	kind, name, ok := detector.DirectOwner(pod)
	if !ok {
		return workloadRef{Kind: "Pod", Name: pod.Name, UID: pod.UID}, nil
	}

	if kind != "ReplicaSet" {
		return workloadRef{Kind: kind, Name: name}, nil
	}

	rs := &appsv1.ReplicaSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: name}, rs); err != nil {
		if apierrors.IsNotFound(err) {
			return workloadRef{Kind: kind, Name: name}, nil
		}
		return workloadRef{}, err
	}

	for i := range rs.OwnerReferences {
		ref := &rs.OwnerReferences[i]
		if ref.Controller != nil && *ref.Controller {
			return workloadRef{Kind: ref.Kind, Name: ref.Name, UID: ref.UID}, nil
		}
	}
	return workloadRef{Kind: kind, Name: name, UID: rs.UID}, nil
}

// policyFor returns the policy governing a namespace, or nil when the namespace has not opted in.
func (r *PodReconciler) policyFor(ctx context.Context, namespace string) (*healingv1alpha1.HealingPolicy, error) {
	policies := &healingv1alpha1.HealingPolicyList{}
	if err := r.List(ctx, policies, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	if len(policies.Items) == 0 {
		return nil, nil
	}
	for i := range policies.Items {
		if policies.Items[i].Name == defaultPolicyName {
			return &policies.Items[i], nil
		}
	}
	return &policies.Items[0], nil
}

// policyCovers reports whether a policy handles a failure type. An empty list means all types.
func policyCovers(policy *healingv1alpha1.HealingPolicy, failureType healingv1alpha1.FailureType) bool {
	if len(policy.Spec.FailureTypes) == 0 {
		return true
	}
	for _, t := range policy.Spec.FailureTypes {
		if t == failureType {
			return true
		}
	}
	return false
}

// withinRateLimit bounds how many diagnoses a namespace may open per hour, so that a broadly
// unhealthy cluster cannot flood the API server or an analysis budget.
func (r *PodReconciler) withinRateLimit(ctx context.Context, namespace string, policy *healingv1alpha1.HealingPolicy) (bool, error) {
	limit := policy.Spec.MaxDiagnosesPerHour
	if limit <= 0 {
		return true, nil
	}

	list := &healingv1alpha1.DiagnosisList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return false, err
	}

	cutoff := time.Now().Add(-time.Hour)
	recent := int32(0)
	for i := range list.Items {
		if list.Items[i].CreationTimestamp.After(cutoff) {
			recent++
		}
	}
	return recent < limit, nil
}

func (r *PodReconciler) buildDiagnosis(
	pod *corev1.Pod,
	workload workloadRef,
	signal *detector.Signal,
	policy *healingv1alpha1.HealingPolicy,
	name, signature string,
) *healingv1alpha1.Diagnosis {
	diagnosis := &healingv1alpha1.Diagnosis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pod.Namespace,
			Labels: map[string]string{
				"kubecure.io/failure-type": string(signal.FailureType),
				"kubecure.io/workload":     workload.Name,
				"kubecure.io/signature":    signature,
			},
		},
		Spec: healingv1alpha1.DiagnosisSpec{
			Target: healingv1alpha1.TargetRef{
				Name: pod.Name,
				UID:  pod.UID,
			},
			FailureType: signal.FailureType,
			Container:   signal.Container,
			Signature:   signature,
			ObservedAt:  metav1.Now(),
			PolicyRef:   policy.Name,
		},
	}

	if workload.Kind != "Pod" {
		diagnosis.Spec.Target.Owner = &healingv1alpha1.OwnerRef{
			Kind: workload.Kind,
			Name: workload.Name,
		}
	}
	return diagnosis
}

// podStatusChanged filters out pod events that cannot change a classification. Pods update
// constantly for reasons unrelated to health, and the detector runs on every event in the cluster.
func podStatusChanged() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, okOld := e.ObjectOld.(*corev1.Pod)
			newPod, okNew := e.ObjectNew.(*corev1.Pod)
			if !okOld || !okNew {
				return true
			}
			return !equality.Semantic.DeepEqual(oldPod.Status, newPod.Status)
		},
	}
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(podStatusChanged())).
		Named("detector").
		Complete(r)
}

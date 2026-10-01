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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
	"github.com/adityax25/KubeCure/internal/evidence"
)

// Scope is what one investigation may read. Every pod named in a tool call is checked against it.
type Scope struct {
	// Namespace is fixed for the life of the broker. No tool accepts a namespace argument, so a
	// caller cannot even express a read elsewhere.
	Namespace string

	// DiagnosisName identifies the diagnosis under investigation, so that history searches exclude
	// it from its own results.
	DiagnosisName string

	FailureType healingv1alpha1.FailureType

	// Container is the default container for container scoped reads.
	Container string

	// TargetPod is the pod originally diagnosed. It may since have been deleted, for instance by a
	// rollout, in which case reads fall back to a live pod of the same workload.
	TargetPod string

	// OwnerKind and OwnerName identify the workload. Any pod controlled by it is in scope; nothing
	// else is.
	OwnerKind string
	OwnerName string

	// MaxCalls is the broker's own ceiling on calls, independent of the agent's budget.
	MaxCalls int
}

// ScopeForDiagnosis derives an investigation's scope from the diagnosis it concerns.
func ScopeForDiagnosis(d *healingv1alpha1.Diagnosis, maxCalls int) Scope {
	scope := Scope{
		Namespace:     d.Namespace,
		DiagnosisName: d.Name,
		FailureType:   d.Spec.FailureType,
		Container:     d.Spec.Container,
		TargetPod:     d.Spec.Target.Name,
		OwnerKind:     "Pod",
		OwnerName:     d.Spec.Target.Name,
		MaxCalls:      maxCalls,
	}
	if owner := d.Spec.Target.Owner; owner != nil {
		scope.OwnerKind = owner.Kind
		scope.OwnerName = owner.Name
	}
	return scope
}

// resolvePod returns the named pod after confirming it belongs to the bound workload. An empty name
// means the diagnosed pod, or a live replacement when a rollout has removed it, which keeps a long
// lived diagnosis usable after the pod it first described is gone.
func (b *Broker) resolvePod(ctx context.Context, name string) (*corev1.Pod, error) {
	if name == "" {
		return b.defaultPod(ctx)
	}

	pod := &corev1.Pod{}
	key := types.NamespacedName{Namespace: b.scope.Namespace, Name: name}
	if err := b.registry.client.Get(ctx, key, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, denied("pod %q does not exist in the investigated workload", name)
		}
		return nil, err
	}

	member, err := b.inWorkload(ctx, pod)
	if err != nil {
		return nil, err
	}
	if !member {
		return nil, denied("pod %q is outside the investigated workload %s/%s",
			name, b.scope.OwnerKind, b.scope.OwnerName)
	}
	return pod, nil
}

func (b *Broker) defaultPod(ctx context.Context) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	key := types.NamespacedName{Namespace: b.scope.Namespace, Name: b.scope.TargetPod}
	err := b.registry.client.Get(ctx, key, pod)
	if err == nil {
		return pod, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	pods := &corev1.PodList{}
	if err := b.registry.client.List(ctx, pods, client.InNamespace(b.scope.Namespace)); err != nil {
		return nil, err
	}
	for i := range pods.Items {
		member, err := b.inWorkload(ctx, &pods.Items[i])
		if err != nil {
			return nil, err
		}
		if member {
			return &pods.Items[i], nil
		}
	}
	return nil, denied("the diagnosed pod is gone and the workload has no remaining pods")
}

// inWorkload reports whether a pod is controlled by the bound workload, walking through the
// intermediate ReplicaSet for Deployments.
func (b *Broker) inWorkload(ctx context.Context, pod *corev1.Pod) (bool, error) {
	if pod.Namespace != b.scope.Namespace {
		return false, nil
	}
	if b.scope.OwnerKind == "Pod" {
		return pod.Name == b.scope.OwnerName, nil
	}

	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return false, nil
	}
	if ref.Kind == b.scope.OwnerKind && ref.Name == b.scope.OwnerName {
		return true, nil
	}
	if ref.Kind != "ReplicaSet" || b.scope.OwnerKind != "Deployment" {
		return false, nil
	}

	rs := &appsv1.ReplicaSet{}
	key := types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}
	if err := b.registry.client.Get(ctx, key, rs); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	owner := metav1.GetControllerOf(rs)
	return owner != nil && owner.Kind == "Deployment" && owner.Name == b.scope.OwnerName, nil
}

// target assembles the collector input for a pod in scope.
func (b *Broker) target(pod *corev1.Pod, container string) evidence.Target {
	if container == "" {
		container = b.scope.Container
	}
	return evidence.Target{
		Pod:         pod,
		Container:   container,
		FailureType: b.scope.FailureType,
		OwnerKind:   b.scope.OwnerKind,
		OwnerName:   b.scope.OwnerName,
	}
}

// workloadTarget is used by workload level reads, which need only the namespace and the owner and
// must keep working after every pod of the workload has been replaced.
func (b *Broker) workloadTarget() evidence.Target {
	return evidence.Target{
		Pod:         &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: b.scope.Namespace, Name: b.scope.TargetPod}},
		Container:   b.scope.Container,
		FailureType: b.scope.FailureType,
		OwnerKind:   b.scope.OwnerKind,
		OwnerName:   b.scope.OwnerName,
	}
}

// hasContainer reports whether a pod declares the named container, so a request for a container
// that does not exist is refused rather than forwarded.
func hasContainer(pod *corev1.Pod, name string) bool {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return true
		}
	}
	return false
}

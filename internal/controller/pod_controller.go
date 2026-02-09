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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// FailureType represents the type of pod failure detected
type FailureType string

const (
	FailureCrashLoopBackOff         FailureType = "CrashLoopBackOff"
	FailureImagePullBackOff         FailureType = "ImagePullBackOff"
	FailureOOMKilled                FailureType = "OOMKilled"
	FailureCreateContainerConfigErr FailureType = "CreateContainerConfigError"
	FailureRunContainerError        FailureType = "RunContainerError"
	FailureEvicted                  FailureType = "Evicted"
	FailureError                    FailureType = "Error"
	FailureUnknown                  FailureType = "Unknown"
)

// PodFailure contains details about a detected pod failure
type PodFailure struct {
	PodName       string
	Namespace     string
	FailureType   FailureType
	ContainerName string
	Message       string
	RestartCount  int32
}

// PodReconciler reconciles a Pod object
type PodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=pods/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=pods/log,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch

// Reconcile is called when a Pod changes. It checks for failures and logs them.
func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch the Pod
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		// Pod was deleted, ignore
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Skip system namespaces (kube-system, kube-public, etc.)
	if isSystemNamespace(pod.Namespace) {
		return ctrl.Result{}, nil
	}

	// 3. Check for failures
	failure := detectFailure(pod)
	if failure == nil {
		// Pod is healthy, nothing to do
		return ctrl.Result{}, nil
	}

	// 4. Log the failure (later: send to AI, create PR)
	log.Info("🚨 Pod failure detected",
		"pod", failure.PodName,
		"namespace", failure.Namespace,
		"failureType", failure.FailureType,
		"container", failure.ContainerName,
		"message", failure.Message,
		"restartCount", failure.RestartCount,
	)

	// TODO: Phase 4 - Aggregate context (logs, events, manifests)
	// TODO: Phase 5 - Send to Gemini AI for diagnosis
	// TODO: Phase 6 - Create GitHub PR or Issue

	return ctrl.Result{}, nil
}

// detectFailure checks a Pod for known failure conditions
func detectFailure(pod *corev1.Pod) *PodFailure {
	// Check container statuses for failures
	for _, cs := range pod.Status.ContainerStatuses {
		// Check waiting state (CrashLoopBackOff, ImagePullBackOff, etc.)
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if isFailureReason(reason) {
				return &PodFailure{
					PodName:       pod.Name,
					Namespace:     pod.Namespace,
					FailureType:   mapReasonToFailureType(reason),
					ContainerName: cs.Name,
					Message:       cs.State.Waiting.Message,
					RestartCount:  cs.RestartCount,
				}
			}
		}

		// Check terminated state (OOMKilled, Error)
		if cs.State.Terminated != nil {
			reason := cs.State.Terminated.Reason
			if reason == "OOMKilled" || reason == "Error" {
				return &PodFailure{
					PodName:       pod.Name,
					Namespace:     pod.Namespace,
					FailureType:   mapReasonToFailureType(reason),
					ContainerName: cs.Name,
					Message:       cs.State.Terminated.Message,
					RestartCount:  cs.RestartCount,
				}
			}
		}
	}

	// Check for evicted pods (shown in pod.Status.Reason)
	if pod.Status.Reason == "Evicted" {
		return &PodFailure{
			PodName:     pod.Name,
			Namespace:   pod.Namespace,
			FailureType: FailureEvicted,
			Message:     pod.Status.Message,
		}
	}

	// Check overall pod phase
	if pod.Status.Phase == corev1.PodFailed {
		return &PodFailure{
			PodName:     pod.Name,
			Namespace:   pod.Namespace,
			FailureType: FailureUnknown,
			Message:     pod.Status.Message,
		}
	}

	return nil
}

// isFailureReason checks if a waiting reason indicates a failure
func isFailureReason(reason string) bool {
	failureReasons := []string{
		"CrashLoopBackOff",
		"ImagePullBackOff",
		"ErrImagePull",
		"CreateContainerConfigError",
		"InvalidImageName",
		"RunContainerError",
	}
	for _, fr := range failureReasons {
		if reason == fr {
			return true
		}
	}
	return false
}

// mapReasonToFailureType converts a Kubernetes reason to our FailureType
func mapReasonToFailureType(reason string) FailureType {
	switch reason {
	case "CrashLoopBackOff":
		return FailureCrashLoopBackOff
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
		return FailureImagePullBackOff
	case "OOMKilled":
		return FailureOOMKilled
	case "CreateContainerConfigError":
		return FailureCreateContainerConfigErr
	case "RunContainerError":
		return FailureRunContainerError
	case "Evicted":
		return FailureEvicted
	case "Error":
		return FailureError
	default:
		return FailureUnknown
	}
}

// isSystemNamespace returns true for Kubernetes system namespaces
func isSystemNamespace(ns string) bool {
	systemNamespaces := []string{
		"kube-system",
		"kube-public",
		"kube-node-lease",
		"local-path-storage", // kind-specific
	}
	for _, sns := range systemNamespaces {
		if ns == sns {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Named("pod").
		Complete(r)
}

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

package detector

import (
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

var now = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) metav1.Time { return metav1.NewTime(now.Add(-d)) }

// podBuilder assembles the pod shapes the tests need without repeating struct literals.
type podBuilder struct{ pod *corev1.Pod }

func newPod() *podBuilder {
	return &podBuilder{pod: &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-7d9f8b6c4-xk2mp", Namespace: "default"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers:    []corev1.Container{{Name: "api"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}}
}

func (b *podBuilder) phase(p corev1.PodPhase) *podBuilder {
	b.pod.Status.Phase = p
	return b
}

func (b *podBuilder) status(cs corev1.ContainerStatus) *podBuilder {
	b.pod.Status.ContainerStatuses = append(b.pod.Status.ContainerStatuses, cs)
	return b
}

func (b *podBuilder) readinessProbe(p *corev1.Probe) *podBuilder {
	b.pod.Spec.Containers[0].ReadinessProbe = p
	return b
}

func (b *podBuilder) build() *corev1.Pod { return b.pod }

func waiting(reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  "api",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want healingv1alpha1.FailureType
	}{
		{
			name: "healthy running container yields no signal",
			pod: newPod().status(corev1.ContainerStatus{
				Name:  "api",
				Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(time.Hour)}},
			}).build(),
			want: "",
		},
		{
			name: "memory kill recorded in the previous termination outranks the displayed crash loop",
			pod: newPod().status(corev1.ContainerStatus{
				Name:         "api",
				RestartCount: 7,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137,
				}},
			}).build(),
			want: healingv1alpha1.FailureOOMKilled,
		},
		{
			name: "memory kill in the current termination is reported",
			pod: newPod().status(corev1.ContainerStatus{
				Name:  "api",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
			}).build(),
			want: healingv1alpha1.FailureOOMKilled,
		},
		{
			name: "crash loop without a memory kill stays a crash loop",
			pod: newPod().status(corev1.ContainerStatus{
				Name:         "api",
				RestartCount: 4,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "Error", ExitCode: 1,
				}},
			}).build(),
			want: healingv1alpha1.FailureCrashLoopBackOff,
		},
		{
			name: "image pull back off",
			pod:  newPod().status(waiting("ImagePullBackOff")).build(),
			want: healingv1alpha1.FailureImagePullBackOff,
		},
		{
			name: "image pull error maps to the same type",
			pod:  newPod().status(waiting("ErrImagePull")).build(),
			want: healingv1alpha1.FailureImagePullBackOff,
		},
		{
			name: "invalid image name maps to the same type",
			pod:  newPod().status(waiting("InvalidImageName")).build(),
			want: healingv1alpha1.FailureImagePullBackOff,
		},
		{
			name: "missing config reference",
			pod:  newPod().status(waiting("CreateContainerConfigError")).build(),
			want: healingv1alpha1.FailureCreateContainerConfigError,
		},
		{
			name: "run container error",
			pod:  newPod().status(waiting("RunContainerError")).build(),
			want: healingv1alpha1.FailureRunContainerError,
		},
		{
			name: "transient startup states are not failures",
			pod:  newPod().status(waiting("ContainerCreating")).build(),
			want: "",
		},
		{
			name: "terminated non-zero without a waiting reason is a crash loop",
			pod: newPod().status(corev1.ContainerStatus{
				Name:  "api",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 2}},
			}).build(),
			want: healingv1alpha1.FailureCrashLoopBackOff,
		},
		{
			name: "clean exit is not a failure",
			pod: newPod().status(corev1.ContainerStatus{
				Name:  "api",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}},
			}).build(),
			want: "",
		},
		{
			name: "eviction is reported at the pod level",
			pod: func() *corev1.Pod {
				p := newPod().phase(corev1.PodFailed).build()
				p.Status.Reason = "Evicted"
				p.Status.Message = "The node was low on resource: memory."
				return p
			}(),
			want: healingv1alpha1.FailureEvicted,
		},
		{
			name: "unschedulable past the grace period",
			pod: func() *corev1.Pod {
				p := newPod().phase(corev1.PodPending).build()
				p.Status.Conditions = []corev1.PodCondition{{
					Type:               corev1.PodScheduled,
					Status:             corev1.ConditionFalse,
					Reason:             corev1.PodReasonUnschedulable,
					Message:            "0/1 nodes are available: 1 Insufficient memory.",
					LastTransitionTime: ago(2 * time.Minute),
				}}
				return p
			}(),
			want: healingv1alpha1.FailureUnschedulable,
		},
		{
			name: "unschedulable within the grace period is ignored",
			pod: func() *corev1.Pod {
				p := newPod().phase(corev1.PodPending).build()
				p.Status.Conditions = []corev1.PodCondition{{
					Type:               corev1.PodScheduled,
					Status:             corev1.ConditionFalse,
					Reason:             corev1.PodReasonUnschedulable,
					LastTransitionTime: ago(5 * time.Second),
				}}
				return p
			}(),
			want: "",
		},
		{
			name: "unready past the declared readiness budget",
			pod: newPod().
				readinessProbe(&corev1.Probe{InitialDelaySeconds: 10, PeriodSeconds: 5, FailureThreshold: 3}).
				status(corev1.ContainerStatus{
					Name:  "api",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(5 * time.Minute)}},
				}).build(),
			want: healingv1alpha1.FailureProbeFailure,
		},
		{
			name: "unready within the declared readiness budget is ignored",
			pod: newPod().
				readinessProbe(&corev1.Probe{InitialDelaySeconds: 10, PeriodSeconds: 5, FailureThreshold: 3}).
				status(corev1.ContainerStatus{
					Name:  "api",
					Ready: false,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(20 * time.Second)}},
				}).build(),
			want: "",
		},
		{
			name: "unready with no readiness probe is not a probe failure",
			pod: newPod().status(corev1.ContainerStatus{
				Name:  "api",
				Ready: false,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: ago(time.Hour)}},
			}).build(),
			want: "",
		},
		{
			name: "a terminating pod is ignored",
			pod: func() *corev1.Pod {
				p := newPod().status(waiting("CrashLoopBackOff")).build()
				ts := ago(time.Minute)
				p.DeletionTimestamp = &ts
				return p
			}(),
			want: "",
		},
		{
			name: "a nil pod is ignored",
			pod:  nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.pod, now)

			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected no signal, got %s", got.FailureType)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %s, got no signal", tt.want)
			}
			if got.FailureType != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got.FailureType)
			}
		})
	}
}

// TestClassifyRetainsObservedReason guards the behaviour ADR-013 depends on: the displayed symptom
// is preserved alongside the classified cause rather than discarded.
func TestClassifyRetainsObservedReason(t *testing.T) {
	pod := newPod().status(corev1.ContainerStatus{
		Name:         "api",
		RestartCount: 7,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	}).build()

	got := Classify(pod, now)
	if got == nil {
		t.Fatal("expected a signal")
	}
	if got.Reason != "OOMKilled" {
		t.Errorf("classified reason: expected OOMKilled, got %s", got.Reason)
	}
	if got.ObservedReason != "CrashLoopBackOff" {
		t.Errorf("observed reason: expected CrashLoopBackOff, got %s", got.ObservedReason)
	}
	if got.ExitCode == nil || *got.ExitCode != 137 {
		t.Errorf("expected exit code 137, got %v", got.ExitCode)
	}
	if got.RestartCount != 7 {
		t.Errorf("expected restart count 7, got %d", got.RestartCount)
	}
}

func TestSignatureIsStableAndDistinct(t *testing.T) {
	base := Signature("default", "Deployment", "checkout-api", "api", healingv1alpha1.FailureOOMKilled)

	if again := Signature("default", "Deployment", "checkout-api", "api", healingv1alpha1.FailureOOMKilled); again != base {
		t.Errorf("signature is not stable: %s then %s", base, again)
	}
	if len(base) != signatureLength {
		t.Errorf("expected %d characters, got %d", signatureLength, len(base))
	}

	distinct := map[string]string{
		"namespace":    Signature("payments", "Deployment", "checkout-api", "api", healingv1alpha1.FailureOOMKilled),
		"owner name":   Signature("default", "Deployment", "ledger", "api", healingv1alpha1.FailureOOMKilled),
		"owner kind":   Signature("default", "StatefulSet", "checkout-api", "api", healingv1alpha1.FailureOOMKilled),
		"container":    Signature("default", "Deployment", "checkout-api", "sidecar", healingv1alpha1.FailureOOMKilled),
		"failure type": Signature("default", "Deployment", "checkout-api", "api", healingv1alpha1.FailureCrashLoopBackOff),
	}
	for field, sig := range distinct {
		if sig == base {
			t.Errorf("changing %s did not change the signature", field)
		}
	}
}

func TestDiagnosisNameIsValid(t *testing.T) {
	// A name must satisfy the DNS subdomain rules the API server enforces on object names.
	valid := regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

	tests := []struct{ name, workload string }{
		{"ordinary workload", "checkout-api"},
		{"upper case is lowered", "CheckoutAPI"},
		{"underscores are replaced", "checkout_api_v2"},
		{"leading and trailing separators are trimmed", "--checkout--"},
		{"empty falls back", ""},
		{"over long is truncated", strings.Repeat("a", 400)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiagnosisName(tt.workload, healingv1alpha1.FailureOOMKilled, "a3f21b9c")
			if len(got) > maxNameLength {
				t.Fatalf("name exceeds %d characters: %d", maxNameLength, len(got))
			}
			if !valid.MatchString(got) {
				t.Fatalf("name is not a valid object name: %q", got)
			}
		})
	}
}

func TestDirectOwner(t *testing.T) {
	controller := true
	pod := newPod().build()
	pod.OwnerReferences = []metav1.OwnerReference{
		{Kind: "Something", Name: "not-the-controller"},
		{Kind: "ReplicaSet", Name: "checkout-api-7d9f8b6c4", Controller: &controller},
	}

	kind, name, ok := DirectOwner(pod)
	if !ok || kind != "ReplicaSet" || name != "checkout-api-7d9f8b6c4" {
		t.Fatalf("expected the controlling reference, got %s/%s ok=%v", kind, name, ok)
	}

	if _, _, ok := DirectOwner(newPod().build()); ok {
		t.Error("expected no owner for a bare pod")
	}
}

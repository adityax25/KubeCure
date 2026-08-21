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
	"time"

	corev1 "k8s.io/api/core/v1"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

const (
	// unschedulableGrace is how long a pod may remain unschedulable before it is reported. The
	// PodScheduled condition is a scheduler verdict rather than work in progress, so this only needs
	// to absorb transient capacity changes during a rollout.
	unschedulableGrace = 30 * time.Second

	// probeGraceBuffer is added to a container's declared readiness budget so that detection does
	// not fire at the exact boundary of a probe that is about to succeed.
	probeGraceBuffer = 15 * time.Second

	// Kubernetes probe defaults, applied when a probe omits the corresponding field.
	defaultProbePeriod           = 10 * time.Second
	defaultProbeFailureThreshold = 3
)

// Signal is the classification of a single pod failure. It carries only what can be read from the
// pod itself; logs and events are gathered by a later stage.
type Signal struct {
	FailureType healingv1alpha1.FailureType

	// Container is the failing container, and is empty for pod level failures such as eviction and
	// unschedulability, where no single container is responsible.
	Container string

	// Reason is the raw Kubernetes reason string that produced the classification.
	Reason string

	// ObservedReason is the reason the cluster currently displays, retained when it differs from the
	// classified cause. A container killed for exceeding its memory limit is displayed as
	// CrashLoopBackOff while its cause is OOMKilled.
	ObservedReason string

	Message      string
	ExitCode     *int32
	RestartCount int32
}

// Classify examines a pod and returns the failure it exhibits, or nil when nothing is wrong.
//
// The function is pure: it performs no I/O and reads no state beyond the pod passed in. It is
// invoked on every pod event in the cluster, including healthy ones, so it must stay cheap.
//
// Check order is significant, and follows ADR-013: the most specific cause wins over the symptom
// the cluster displays. A container killed for exceeding its memory limit is almost never observed
// in a terminated state, because the kubelet restarts it and reports CrashLoopBackOff. The original
// cause survives only in the previous termination record, so that record is consulted first.
func Classify(pod *corev1.Pod, now time.Time) *Signal {
	if pod == nil || pod.DeletionTimestamp != nil {
		return nil
	}

	if pod.Status.Reason == "Evicted" {
		return &Signal{
			FailureType: healingv1alpha1.FailureEvicted,
			Reason:      pod.Status.Reason,
			Message:     pod.Status.Message,
		}
	}

	if s := classifyUnschedulable(pod, now); s != nil {
		return s
	}

	for i := range pod.Status.ContainerStatuses {
		if s := classifyContainer(pod, &pod.Status.ContainerStatuses[i], now); s != nil {
			return s
		}
	}

	return nil
}

// classifyUnschedulable reports a pod the scheduler could not place. The PodScheduled condition is
// used rather than the pod phase, because a pod pulling a large image is also Pending.
func classifyUnschedulable(pod *corev1.Pod, now time.Time) *Signal {
	if pod.Status.Phase != corev1.PodPending {
		return nil
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type != corev1.PodScheduled || cond.Status != corev1.ConditionFalse {
			continue
		}
		if cond.Reason != corev1.PodReasonUnschedulable {
			continue
		}
		if now.Sub(cond.LastTransitionTime.Time) < unschedulableGrace {
			return nil
		}
		return &Signal{
			FailureType: healingv1alpha1.FailureUnschedulable,
			Reason:      cond.Reason,
			Message:     cond.Message,
		}
	}

	return nil
}

// classifyContainer maps one container's status to a failure type, applying the precedence in
// ADR-013.
func classifyContainer(pod *corev1.Pod, cs *corev1.ContainerStatus, now time.Time) *Signal {
	if term := cs.LastTerminationState.Terminated; term != nil && term.Reason == reasonOOMKilled {
		return &Signal{
			FailureType:    healingv1alpha1.FailureOOMKilled,
			Container:      cs.Name,
			Reason:         term.Reason,
			ObservedReason: currentReason(cs),
			Message:        term.Message,
			ExitCode:       ptr(term.ExitCode),
			RestartCount:   cs.RestartCount,
		}
	}

	if term := cs.State.Terminated; term != nil && term.Reason == reasonOOMKilled {
		return &Signal{
			FailureType:  healingv1alpha1.FailureOOMKilled,
			Container:    cs.Name,
			Reason:       term.Reason,
			Message:      term.Message,
			ExitCode:     ptr(term.ExitCode),
			RestartCount: cs.RestartCount,
		}
	}

	if wait := cs.State.Waiting; wait != nil {
		if ft, ok := waitingReasonToFailureType(wait.Reason); ok {
			return &Signal{
				FailureType:  ft,
				Container:    cs.Name,
				Reason:       wait.Reason,
				Message:      wait.Message,
				ExitCode:     lastExitCode(cs),
				RestartCount: cs.RestartCount,
			}
		}
	}

	if term := cs.State.Terminated; term != nil && term.ExitCode != 0 &&
		pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return &Signal{
			FailureType:  healingv1alpha1.FailureCrashLoopBackOff,
			Container:    cs.Name,
			Reason:       term.Reason,
			Message:      term.Message,
			ExitCode:     ptr(term.ExitCode),
			RestartCount: cs.RestartCount,
		}
	}

	return classifyProbeFailure(pod, cs, now)
}

// classifyProbeFailure reports a container that runs but never becomes ready, which is the shape a
// persistently failing readiness probe takes. A failing liveness probe restarts the container
// instead, so it surfaces as a crash loop and is separated later using events.
//
// The threshold comes from the container's own probe configuration, per ADR-014.
func classifyProbeFailure(pod *corev1.Pod, cs *corev1.ContainerStatus, now time.Time) *Signal {
	run := cs.State.Running
	if run == nil || cs.Ready {
		return nil
	}

	grace, ok := readinessBudget(pod, cs.Name)
	if !ok {
		return nil
	}
	if now.Sub(run.StartedAt.Time) < grace {
		return nil
	}

	return &Signal{
		FailureType:  healingv1alpha1.FailureProbeFailure,
		Container:    cs.Name,
		Reason:       "ReadinessProbeFailed",
		Message:      "container has been running past its declared readiness budget without becoming ready",
		RestartCount: cs.RestartCount,
	}
}

// readinessBudget returns how long a container is permitted to remain unready, derived from its
// declared readiness probe. The second return value is false when the container declares no
// readiness probe, in which case readiness follows container start and cannot indicate a probe
// failure.
func readinessBudget(pod *corev1.Pod, container string) (time.Duration, bool) {
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name != container {
			continue
		}
		probe := c.ReadinessProbe
		if probe == nil {
			return 0, false
		}

		period := time.Duration(probe.PeriodSeconds) * time.Second
		if period <= 0 {
			period = defaultProbePeriod
		}
		threshold := probe.FailureThreshold
		if threshold <= 0 {
			threshold = defaultProbeFailureThreshold
		}
		initial := time.Duration(probe.InitialDelaySeconds) * time.Second

		return initial + time.Duration(threshold)*period + probeGraceBuffer, true
	}
	return 0, false
}

const reasonOOMKilled = "OOMKilled"

// waitingReasonToFailureType maps a kubelet waiting reason to a failure type. Reasons absent from
// this table are transient startup states rather than failures.
func waitingReasonToFailureType(reason string) (healingv1alpha1.FailureType, bool) {
	switch reason {
	case "CrashLoopBackOff":
		return healingv1alpha1.FailureCrashLoopBackOff, true
	case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "ImageInspectError", "ErrImageNeverPull":
		return healingv1alpha1.FailureImagePullBackOff, true
	case "CreateContainerConfigError":
		return healingv1alpha1.FailureCreateContainerConfigError, true
	case "RunContainerError", "CreateContainerError":
		return healingv1alpha1.FailureRunContainerError, true
	default:
		return "", false
	}
}

// currentReason returns the reason the cluster is presently displaying for a container, which is
// retained on the signal when it differs from the classified cause.
func currentReason(cs *corev1.ContainerStatus) string {
	if wait := cs.State.Waiting; wait != nil {
		return wait.Reason
	}
	if term := cs.State.Terminated; term != nil {
		return term.Reason
	}
	return ""
}

// lastExitCode returns the previous instance's exit status, which is the only record of why a crash
// looping container stopped.
func lastExitCode(cs *corev1.ContainerStatus) *int32 {
	if term := cs.LastTerminationState.Terminated; term != nil {
		return ptr(term.ExitCode)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

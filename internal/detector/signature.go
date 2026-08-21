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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

const (
	// signatureLength is the number of hex characters retained from the signature hash. Thirty two
	// bits keeps names readable while leaving collisions far beyond the volume a namespace rate
	// limit permits.
	signatureLength = 8

	// maxNameLength is the Kubernetes limit for an object name.
	maxNameLength = 253

	// maxWorkloadSegment bounds the workload portion of a generated name so that the failure type
	// and signature always survive truncation.
	maxWorkloadSegment = 180
)

var nonNameChars = regexp.MustCompile(`[^a-z0-9.-]+`)

// Signature produces the deduplication key for a failure, per ADR-008. Replicas of the same
// workload failing the same way in the same container produce an identical signature, so they
// collapse into a single diagnosis and a single proposed fix.
//
// The owner is supplied by the caller rather than read from the pod, because resolving a pod's
// ReplicaSet up to its Deployment requires an API call and this package performs no I/O.
func Signature(namespace, ownerKind, ownerName, container string, failureType healingv1alpha1.FailureType) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		namespace, ownerKind, ownerName, container, string(failureType),
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:signatureLength]
}

// DirectOwner returns the controlling owner reference of a pod. For a pod managed by a Deployment
// this is the intermediate ReplicaSet; resolving further up the chain requires an API call and is
// the caller's responsibility.
func DirectOwner(pod *corev1.Pod) (kind, name string, ok bool) {
	for i := range pod.OwnerReferences {
		ref := &pod.OwnerReferences[i]
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind, ref.Name, true
		}
	}
	return "", "", false
}

// DiagnosisName builds the object name for a diagnosis. The name is derived deterministically from
// the signature, so a repeated creation attempt for the same failure is rejected by the API server
// as already existing, which is what enforces deduplication without any local state.
func DiagnosisName(workload string, failureType healingv1alpha1.FailureType, signature string) string {
	workload = sanitizeNameSegment(workload)
	if workload == "" {
		workload = "pod"
	}
	if len(workload) > maxWorkloadSegment {
		workload = strings.Trim(workload[:maxWorkloadSegment], "-.")
	}

	name := fmt.Sprintf("%s-%s-%s", workload, sanitizeNameSegment(string(failureType)), signature)
	if len(name) > maxNameLength {
		name = name[:maxNameLength]
	}
	return name
}

// sanitizeNameSegment reduces a string to the characters permitted in a Kubernetes object name.
func sanitizeNameSegment(s string) string {
	s = strings.ToLower(s)
	s = nonNameChars.ReplaceAllString(s, "-")
	return strings.Trim(s, "-.")
}

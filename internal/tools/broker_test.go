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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

const ns = "demo"

func controllerRef(kind, name string, uid types.UID) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid, Controller: &yes}}
}

// workload builds a Deployment, its ReplicaSet, and one pod, wired by controller references the way
// the Deployment controller wires them.
func workload(name, command string) []client.Object {
	deployUID := types.UID(name + "-deploy-uid")
	rsName := name + "-7d9f8b6c4"
	labels := map[string]string{"app": name}

	template := corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "api",
		Image:   "busybox:1.36",
		Command: []string{"sh", "-c", command},
	}}}

	return []client.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: deployUID,
				Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"}},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: template},
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: rsName, Namespace: ns, UID: types.UID(rsName + "-uid"),
				Labels: labels, OwnerReferences: controllerRef("Deployment", name, deployUID),
				Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"}},
			Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: template}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: rsName + "-x1", Namespace: ns, Labels: labels,
				OwnerReferences: controllerRef("ReplicaSet", rsName, types.UID(rsName+"-uid"))},
			Spec: template,
		},
	}
}

func newBroker(t *testing.T, scope Scope, objects ...client.Object) *Broker {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := healingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return NewRegistry(c, clientsetfake.NewSimpleClientset()).BindTo(scope)
}

func checkoutScope() Scope {
	return Scope{
		Namespace:   ns,
		FailureType: healingv1alpha1.FailureCrashLoopBackOff,
		Container:   "api",
		TargetPod:   "checkout-7d9f8b6c4-x1",
		OwnerKind:   "Deployment",
		OwnerName:   "checkout",
	}
}

func fixtures() []client.Object {
	objs := workload("checkout", "echo connecting with password=hunter2-not-real; exit 1")
	return append(objs, workload("billing", "sleep 600")...)
}

func call(t *testing.T, b *Broker, name, args string) (string, error) {
	t.Helper()
	return b.Call(context.Background(), name, json.RawMessage(args))
}

func TestCatalogueIsReadOnly(t *testing.T) {
	b := newBroker(t, checkoutScope())
	for _, spec := range b.Tools() {
		for _, verb := range []string{"set_", "patch", "delete", "update", "create", "apply", "scale", "restart"} {
			if strings.Contains(spec.Name, verb) {
				t.Errorf("tool %q looks mutating; the catalogue must be read only", spec.Name)
			}
		}
		var schema map[string]any
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Errorf("tool %q has an invalid input schema: %v", spec.Name, err)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("tool %q schema must disallow additional properties", spec.Name)
		}
	}
	if got := len(b.Tools()); got != 10 {
		t.Errorf("expected 10 tools, got %d", got)
	}
}

func TestDenials(t *testing.T) {
	tests := []struct {
		name, tool, args, reason string
	}{
		{"unknown tool", "delete_namespace", `{}`, "unknown tool"},
		{"undeclared argument cannot smuggle a namespace", "get_pod_events", `{"namespace":"kube-system"}`, "invalid arguments"},
		{"pod belonging to another workload", "get_pod_events", `{"pod":"billing-7d9f8b6c4-x1"}`, "outside the investigated workload"},
		{"pod that does not exist", "get_pod_events", `{"pod":"kube-apiserver"}`, "does not exist"},
		{"container the pod does not declare", "get_pod_logs", `{"container":"sidecar"}`, "has no container"},
		{"malformed arguments", "get_pod_events", `{"pod":`, "invalid arguments"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBroker(t, checkoutScope(), fixtures()...)
			_, err := call(t, b, tt.tool, tt.args)
			if err == nil {
				t.Fatal("expected the call to be denied")
			}
			if !IsDenied(err) {
				t.Fatalf("expected a denial, got a read failure: %v", err)
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("expected reason containing %q, got %v", tt.reason, err)
			}
		})
	}
}

func TestDeniedCallsAreAudited(t *testing.T) {
	b := newBroker(t, checkoutScope(), fixtures()...)
	_, _ = call(t, b, "get_pod_events", `{"pod":"billing-7d9f8b6c4-x1"}`)

	calls := b.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one audited call, got %d", len(calls))
	}
	if calls[0].Error == "" || calls[0].Tool != "get_pod_events" {
		t.Fatalf("expected the denial to be recorded, got %+v", calls[0])
	}
	if !strings.Contains(calls[0].Arguments, "billing") {
		t.Errorf("expected the attempted arguments to be recorded, got %q", calls[0].Arguments)
	}
}

func TestResultsAreRedacted(t *testing.T) {
	b := newBroker(t, checkoutScope(), fixtures()...)
	result, err := call(t, b, "get_workload_spec", `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "hunter2") {
		t.Fatal("a credential reached the caller unredacted")
	}
	if !strings.Contains(result, "REDACTED") {
		t.Fatal("expected a redaction marker in the result")
	}
}

func TestCallCeiling(t *testing.T) {
	scope := checkoutScope()
	scope.MaxCalls = 2
	b := newBroker(t, scope, fixtures()...)

	for i := 0; i < 2; i++ {
		if _, err := call(t, b, "get_workload_spec", `{}`); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
	}
	_, err := call(t, b, "get_workload_spec", `{}`)
	if !IsDenied(err) || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("expected the third call to hit the ceiling, got %v", err)
	}
}

func TestFallsBackToLivePodAfterRollout(t *testing.T) {
	scope := checkoutScope()
	scope.TargetPod = "checkout-5c8b9f2a1-gone"
	b := newBroker(t, scope, fixtures()...)

	pod, err := b.resolvePod(context.Background(), "")
	if err != nil {
		t.Fatalf("expected a live replacement pod, got %v", err)
	}
	if pod.Name != "checkout-7d9f8b6c4-x1" {
		t.Fatalf("expected the workload's live pod, got %s", pod.Name)
	}
}

func TestSimilarDiagnosesStayInNamespace(t *testing.T) {
	past := func(name, namespace string) *healingv1alpha1.Diagnosis {
		return &healingv1alpha1.Diagnosis{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       healingv1alpha1.DiagnosisSpec{FailureType: healingv1alpha1.FailureCrashLoopBackOff},
		}
	}
	scope := checkoutScope()
	scope.DiagnosisName = "current"
	b := newBroker(t, scope, past("current", ns), past("earlier", ns), past("elsewhere", "other-team"))

	result, err := call(t, b, "search_similar_diagnoses", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "earlier") {
		t.Error("expected the earlier diagnosis in the same namespace")
	}
	if strings.Contains(result, "elsewhere") {
		t.Error("a diagnosis from another namespace leaked into the results")
	}
	if strings.Contains(result, `"current"`) {
		t.Error("the diagnosis under investigation should not match itself")
	}
}

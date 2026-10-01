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

package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	healingv1alpha1 "github.com/adityax25/KubeCure/api/v1alpha1"
)

const (
	// contentLimit matches the schema's per item ceiling.
	contentLimit = 8192
	// logTailLines bounds how much container output is requested.
	logTailLines = 100
	// revisionsReported is how far back rollout history is summarised. Recent revisions establish
	// whether a change preceded the failure; older ones rarely inform a diagnosis.
	revisionsReported = 3
	// revisionAnnotation is where the Deployment controller records a ReplicaSet's revision number.
	revisionAnnotation = "deployment.kubernetes.io/revision"
)

// Target identifies what is being investigated and what kind of failure it exhibits.
type Target struct {
	Pod         *corev1.Pod
	Container   string
	FailureType healingv1alpha1.FailureType
	OwnerKind   string
	OwnerName   string
}

// Collector gathers diagnostic context against the requirement matrix.
//
// It needs two clients. The cached controller-runtime client serves ordinary object reads, while
// container logs are a subresource that the cache cannot serve and which must be fetched through a
// direct clientset.
type Collector struct {
	Client    client.Client
	Clientset kubernetes.Interface
}

// Collect gathers every applicable evidence kind for a failure and reports the outcome of each,
// including those that could not or need not exist. Reporting the full catalogue is what
// distinguishes evidence that cannot exist from evidence that was never sought (ADR-016).
func (c *Collector) Collect(ctx context.Context, target Target) *healingv1alpha1.Evidence {
	ev := &healingv1alpha1.Evidence{}
	populateContainerFacts(ev, target)

	var totalRedactions int32

	for _, kind := range allKinds {
		requirement := RequirementFor(target.FailureType, kind)

		if requirement == NotApplicable {
			ev.Items = append(ev.Items, healingv1alpha1.EvidenceItem{
				Kind:   kind,
				Status: healingv1alpha1.CollectionNotApplicable,
				Reason: NotApplicableReason(target.FailureType, kind),
			})
			continue
		}

		item := healingv1alpha1.EvidenceItem{Kind: kind, Required: requirement == Required}

		content, err := c.gather(ctx, target, kind)
		switch {
		case err != nil:
			item.Status = healingv1alpha1.CollectionUnavailable
			item.Reason = err.Error()
		case strings.TrimSpace(content) == "":
			item.Status = healingv1alpha1.CollectionUnavailable
			item.Reason = "no content returned"
		default:
			redacted, count := Redact(content)
			totalRedactions += count
			item.Status = healingv1alpha1.CollectionCollected
			item.Content = Truncate(redacted, contentLimit)
		}

		if item.Required && item.Status != healingv1alpha1.CollectionCollected {
			ev.MissingRequired = append(ev.MissingRequired, kind)
		}
		ev.Items = append(ev.Items, item)
	}

	ev.RedactedFields = totalRedactions
	ev.Complete = len(ev.MissingRequired) == 0
	return ev
}

// populateContainerFacts lifts the scalars already present on the pod, so that a consumer does not
// have to parse them back out of gathered text.
func populateContainerFacts(ev *healingv1alpha1.Evidence, target Target) {
	for i := range target.Pod.Status.ContainerStatuses {
		cs := &target.Pod.Status.ContainerStatuses[i]
		if target.Container != "" && cs.Name != target.Container {
			continue
		}
		ev.RestartCount = cs.RestartCount
		if term := cs.LastTerminationState.Terminated; term != nil {
			ev.TerminationReason = term.Reason
			code := term.ExitCode
			ev.ExitCode = &code
		} else if term := cs.State.Terminated; term != nil {
			ev.TerminationReason = term.Reason
			code := term.ExitCode
			ev.ExitCode = &code
		}
		if wait := cs.State.Waiting; wait != nil {
			ev.WaitingReason = wait.Reason
		}
		return
	}
}

// Gather fetches a single evidence kind for a target, unredacted and untruncated. It is the shared
// read path for both baseline collection and the tool broker, so the two can never diverge in what
// they return for the same request. Callers are responsible for scrubbing the result.
func (c *Collector) Gather(ctx context.Context, target Target, kind healingv1alpha1.EvidenceKind) (string, error) {
	return c.gather(ctx, target, kind)
}

func (c *Collector) gather(ctx context.Context, target Target, kind healingv1alpha1.EvidenceKind) (string, error) {
	switch kind {
	case healingv1alpha1.EvidencePreviousLogs:
		return c.logs(ctx, target, true)
	case healingv1alpha1.EvidenceCurrentLogs:
		return c.logs(ctx, target, false)
	case healingv1alpha1.EvidenceEvents:
		return c.events(ctx, target)
	case healingv1alpha1.EvidenceWorkloadSpec:
		return c.workloadSpec(ctx, target)
	case healingv1alpha1.EvidenceRolloutHistory:
		return c.rolloutHistory(ctx, target)
	case healingv1alpha1.EvidenceConfigReferences:
		return c.configReferences(ctx, target)
	case healingv1alpha1.EvidenceServiceEndpoints:
		return c.serviceEndpoints(ctx, target)
	case healingv1alpha1.EvidenceNodeStatus:
		return c.nodeStatus(ctx, target)
	case healingv1alpha1.EvidenceSiblingPods:
		return c.siblingPods(ctx, target)
	case healingv1alpha1.EvidenceResourceUsage:
		return c.resourceUsage(ctx, target)
	}
	return "", fmt.Errorf("no collector for %s", kind)
}

// logs fetches container output. The previous instance is the important one for a restarting
// container, since its output contains the error that preceded termination.
func (c *Collector) logs(ctx context.Context, target Target, previous bool) (string, error) {
	tail := int64(logTailLines)
	opts := &corev1.PodLogOptions{
		Container: target.Container,
		Previous:  previous,
		TailLines: &tail,
	}

	stream, err := c.Clientset.CoreV1().
		Pods(target.Pod.Namespace).
		GetLogs(target.Pod.Name, opts).
		Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("reading logs: %w", summarise(err))
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("reading logs: %w", err)
	}
	return string(data), nil
}

// events returns the pod's warning events, most recent first. Scheduler verdicts and missing object
// names appear here and nowhere else.
func (c *Collector) events(ctx context.Context, target Target) (string, error) {
	selector := fields.OneTermEqualSelector("involvedObject.name", target.Pod.Name).String()
	list, err := c.Clientset.CoreV1().Events(target.Pod.Namespace).
		List(ctx, metav1.ListOptions{FieldSelector: selector, Limit: 50})
	if err != nil {
		return "", fmt.Errorf("listing events: %w", summarise(err))
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return eventTime(&list.Items[i]).Time.After(eventTime(&list.Items[j]).Time)
	})

	var b strings.Builder
	for i := range list.Items {
		e := &list.Items[i]
		fmt.Fprintf(&b, "%s  %-24s  count=%d  %s\n", e.Type, e.Reason, e.Count, strings.TrimSpace(e.Message))
	}
	return b.String(), nil
}

func eventTime(e *corev1.Event) metav1.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp
	}
	if !e.EventTime.IsZero() {
		return metav1.Time{Time: e.EventTime.Time}
	}
	return e.CreationTimestamp
}

// workloadSpec returns the owning workload's pod template, which carries the fields a remediation
// would change: resource limits, probes, and image references.
func (c *Collector) workloadSpec(ctx context.Context, target Target) (string, error) {
	spec, err := c.podTemplate(ctx, target)
	if err != nil {
		return "", err
	}

	summary := map[string]any{
		"owner":         target.OwnerKind + "/" + target.OwnerName,
		"restartPolicy": string(spec.RestartPolicy),
		"nodeSelector":  spec.NodeSelector,
		"containers":    summariseContainers(spec.Containers),
	}
	if len(spec.Tolerations) > 0 {
		summary["tolerations"] = spec.Tolerations
	}
	return toJSON(summary)
}

func summariseContainers(containers []corev1.Container) []map[string]any {
	out := make([]map[string]any, 0, len(containers))
	for i := range containers {
		c := &containers[i]
		entry := map[string]any{
			"name":      c.Name,
			"image":     c.Image,
			"resources": c.Resources,
		}
		if len(c.Command) > 0 {
			entry["command"] = c.Command
		}
		if len(c.Args) > 0 {
			entry["args"] = c.Args
		}
		if c.ReadinessProbe != nil {
			entry["readinessProbe"] = c.ReadinessProbe
		}
		if c.LivenessProbe != nil {
			entry["livenessProbe"] = c.LivenessProbe
		}
		if len(c.Ports) > 0 {
			entry["ports"] = c.Ports
		}
		out = append(out, entry)
	}
	return out
}

func (c *Collector) podTemplate(ctx context.Context, target Target) (*corev1.PodSpec, error) {
	key := types.NamespacedName{Namespace: target.Pod.Namespace, Name: target.OwnerName}

	switch target.OwnerKind {
	case "Deployment":
		var d appsv1.Deployment
		if err := c.Client.Get(ctx, key, &d); err != nil {
			return nil, summarise(err)
		}
		return &d.Spec.Template.Spec, nil
	case "StatefulSet":
		var s appsv1.StatefulSet
		if err := c.Client.Get(ctx, key, &s); err != nil {
			return nil, summarise(err)
		}
		return &s.Spec.Template.Spec, nil
	case "DaemonSet":
		var d appsv1.DaemonSet
		if err := c.Client.Get(ctx, key, &d); err != nil {
			return nil, summarise(err)
		}
		return &d.Spec.Template.Spec, nil
	default:
		return &target.Pod.Spec, nil
	}
}

// rolloutHistory summarises recent revisions of the owning workload. This is what establishes
// whether a change shortly preceded the failure, which is the difference between proposing a
// rollback and proposing a novel forward fix.
func (c *Collector) rolloutHistory(ctx context.Context, target Target) (string, error) {
	if target.OwnerKind != "Deployment" {
		return "", fmt.Errorf("rollout history is only available for Deployments, owner is %s", target.OwnerKind)
	}

	var deployment appsv1.Deployment
	key := types.NamespacedName{Namespace: target.Pod.Namespace, Name: target.OwnerName}
	if err := c.Client.Get(ctx, key, &deployment); err != nil {
		return "", summarise(err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return "", err
	}

	var replicaSets appsv1.ReplicaSetList
	if err := c.Client.List(ctx, &replicaSets,
		client.InNamespace(target.Pod.Namespace),
		client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return "", summarise(err)
	}

	owned := replicaSets.Items[:0]
	for i := range replicaSets.Items {
		for _, ref := range replicaSets.Items[i].OwnerReferences {
			if ref.UID == deployment.UID {
				owned = append(owned, replicaSets.Items[i])
				break
			}
		}
	}

	sort.Slice(owned, func(i, j int) bool {
		return revisionOf(&owned[i]) > revisionOf(&owned[j])
	})

	revisions := make([]map[string]any, 0, revisionsReported)
	for i := range owned {
		if i >= revisionsReported {
			break
		}
		rs := &owned[i]
		revisions = append(revisions, map[string]any{
			"revision":   revisionOf(rs),
			"created":    rs.CreationTimestamp.Time.UTC().Format("2006-01-02T15:04:05Z"),
			"replicas":   rs.Status.Replicas,
			"containers": summariseContainers(rs.Spec.Template.Spec.Containers),
		})
	}

	return toJSON(map[string]any{
		"currentRevision": deployment.Annotations[revisionAnnotation],
		"revisions":       revisions,
	})
}

func revisionOf(rs *appsv1.ReplicaSet) int {
	n, err := strconv.Atoi(rs.Annotations[revisionAnnotation])
	if err != nil {
		return 0
	}
	return n
}

// configReferences reports whether every ConfigMap and Secret the pod references actually exists.
// Key names are listed so that a missing key can be identified; values are never read.
func (c *Collector) configReferences(ctx context.Context, target Target) (string, error) {
	type reference struct {
		Kind    string   `json:"kind"`
		Name    string   `json:"name"`
		Exists  bool     `json:"exists"`
		Keys    []string `json:"keys,omitempty"`
		Message string   `json:"message,omitempty"`
	}

	seen := map[string]bool{}
	var refs []reference

	record := func(kind, name string) {
		id := kind + "/" + name
		if name == "" || seen[id] {
			return
		}
		seen[id] = true

		ref := reference{Kind: kind, Name: name}
		key := types.NamespacedName{Namespace: target.Pod.Namespace, Name: name}

		if kind == "ConfigMap" {
			var cm corev1.ConfigMap
			if err := c.Client.Get(ctx, key, &cm); err != nil {
				ref.Message = summarise(err).Error()
			} else {
				ref.Exists = true
				for k := range cm.Data {
					ref.Keys = append(ref.Keys, k)
				}
			}
		} else {
			var secret corev1.Secret
			if err := c.Client.Get(ctx, key, &secret); err != nil {
				ref.Message = summarise(err).Error()
			} else {
				ref.Exists = true
				for k := range secret.Data {
					ref.Keys = append(ref.Keys, k)
				}
			}
		}
		sort.Strings(ref.Keys)
		refs = append(refs, ref)
	}

	for i := range target.Pod.Spec.Containers {
		container := &target.Pod.Spec.Containers[i]
		for _, source := range container.EnvFrom {
			if source.ConfigMapRef != nil {
				record("ConfigMap", source.ConfigMapRef.Name)
			}
			if source.SecretRef != nil {
				record("Secret", source.SecretRef.Name)
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom == nil {
				continue
			}
			if r := env.ValueFrom.ConfigMapKeyRef; r != nil {
				record("ConfigMap", r.Name)
			}
			if r := env.ValueFrom.SecretKeyRef; r != nil {
				record("Secret", r.Name)
			}
		}
	}
	for _, volume := range target.Pod.Spec.Volumes {
		if volume.ConfigMap != nil {
			record("ConfigMap", volume.ConfigMap.Name)
		}
		if volume.Secret != nil {
			record("Secret", volume.Secret.SecretName)
		}
	}

	if len(refs) == 0 {
		return "", fmt.Errorf("the pod references no ConfigMaps or Secrets")
	}
	return toJSON(refs)
}

// serviceEndpoints reports the Services selecting this pod and whether it is a member of their
// endpoints. A pod that never becomes ready is absent from every endpoint set, which is the
// mechanism by which a probe failure becomes an outage.
func (c *Collector) serviceEndpoints(ctx context.Context, target Target) (string, error) {
	var services corev1.ServiceList
	if err := c.Client.List(ctx, &services, client.InNamespace(target.Pod.Namespace)); err != nil {
		return "", summarise(err)
	}

	podLabels := labels.Set(target.Pod.Labels)
	var matched []map[string]any

	for i := range services.Items {
		svc := &services.Items[i]
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		if !labels.SelectorFromSet(svc.Spec.Selector).Matches(podLabels) {
			continue
		}

		entry := map[string]any{
			"service": svc.Name,
			"ports":   svc.Spec.Ports,
		}

		var endpoints corev1.Endpoints
		key := types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}
		if err := c.Client.Get(ctx, key, &endpoints); err == nil {
			ready, notReady := 0, 0
			podIsReady := false
			for _, subset := range endpoints.Subsets {
				ready += len(subset.Addresses)
				notReady += len(subset.NotReadyAddresses)
				for _, addr := range subset.Addresses {
					if addr.TargetRef != nil && addr.TargetRef.Name == target.Pod.Name {
						podIsReady = true
					}
				}
			}
			entry["readyEndpoints"] = ready
			entry["notReadyEndpoints"] = notReady
			entry["thisPodIsServing"] = podIsReady
		}
		matched = append(matched, entry)
	}

	if len(matched) == 0 {
		return "", fmt.Errorf("no Service selects this pod")
	}
	return toJSON(matched)
}

// nodeStatus reports the host node's conditions and capacity, which is what separates a pod level
// fault from a node under pressure.
func (c *Collector) nodeStatus(ctx context.Context, target Target) (string, error) {
	nodeName := target.Pod.Spec.NodeName
	if nodeName == "" {
		var nodes corev1.NodeList
		if err := c.Client.List(ctx, &nodes); err != nil {
			return "", summarise(err)
		}
		summary := make([]map[string]any, 0, len(nodes.Items))
		for i := range nodes.Items {
			n := &nodes.Items[i]
			summary = append(summary, map[string]any{
				"node":        n.Name,
				"allocatable": n.Status.Allocatable,
				"taints":      n.Spec.Taints,
				"conditions":  notableConditions(n),
			})
		}
		return toJSON(map[string]any{
			"note":  "the pod is unscheduled; reporting all nodes so capacity can be compared against its requests",
			"nodes": summary,
		})
	}

	var node corev1.Node
	if err := c.Client.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return "", summarise(err)
	}
	return toJSON(map[string]any{
		"node":        node.Name,
		"allocatable": node.Status.Allocatable,
		"taints":      node.Spec.Taints,
		"conditions":  notableConditions(&node),
	})
}

// notableConditions keeps conditions that indicate a problem, plus Ready regardless of value.
func notableConditions(node *corev1.Node) []map[string]string {
	var out []map[string]string
	for _, cond := range node.Status.Conditions {
		interesting := cond.Type == corev1.NodeReady ||
			(cond.Status == corev1.ConditionTrue && cond.Type != corev1.NodeReady)
		if !interesting {
			continue
		}
		out = append(out, map[string]string{
			"type":    string(cond.Type),
			"status":  string(cond.Status),
			"reason":  cond.Reason,
			"message": cond.Message,
		})
	}
	return out
}

// siblingPods reports the state of other pods in the same workload, which distinguishes one
// unhealthy pod from a workload wide fault.
func (c *Collector) siblingPods(ctx context.Context, target Target) (string, error) {
	var pods corev1.PodList
	if err := c.Client.List(ctx, &pods,
		client.InNamespace(target.Pod.Namespace),
		client.MatchingLabels(target.Pod.Labels)); err != nil {
		return "", summarise(err)
	}

	summary := make([]map[string]any, 0, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		entry := map[string]any{
			"pod":   p.Name,
			"phase": string(p.Status.Phase),
			"self":  p.Name == target.Pod.Name,
		}
		if len(p.Status.ContainerStatuses) > 0 {
			cs := &p.Status.ContainerStatuses[0]
			entry["ready"] = cs.Ready
			entry["restarts"] = cs.RestartCount
			if wait := cs.State.Waiting; wait != nil {
				entry["waiting"] = wait.Reason
			}
		}
		summary = append(summary, entry)
	}

	if len(summary) <= 1 {
		return "", fmt.Errorf("the workload has no other pods to compare against")
	}
	return toJSON(summary)
}

// resourceUsage reads observed consumption from the metrics API, which is what allows a limit to be
// sized from evidence rather than by multiplying the current value. Many clusters do not run a
// metrics server, and that absence is reported rather than silently ignored.
func (c *Collector) resourceUsage(ctx context.Context, target Target) (string, error) {
	path := fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods/%s",
		target.Pod.Namespace, target.Pod.Name)

	raw, err := c.Clientset.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("no metrics server is installed in this cluster")
		}
		return "", fmt.Errorf("reading pod metrics: %w", summarise(err))
	}
	return string(raw), nil
}

func toJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// summarise reduces an API error to a short reason suitable for storing on an object, since the
// full error carries request detail that is noise in a diagnosis.
func summarise(err error) error {
	if status := apierrors.APIStatus(nil); apierrors.IsNotFound(err) {
		_ = status
		return fmt.Errorf("not found")
	}
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("access denied by RBAC")
	}
	msg := err.Error()
	if idx := strings.Index(msg, ":"); idx > 0 && len(msg) > 160 {
		msg = msg[:idx]
	}
	return fmt.Errorf("%s", msg)
}

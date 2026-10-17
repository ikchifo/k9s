// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/slogs"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	StatusOK       = "OK"
	DegradedStatus = "DEGRADED"
)

var resList = []*client.GVR{
	client.PodGVR,
	client.SvcGVR,
	client.DsGVR,
	client.StsGVR,
	client.DpGVR,
	client.RsGVR,
}

// Workload tracks a select set of resources in a given namespace.
type Workload struct {
	Table
}

func (w *Workload) Delete(ctx context.Context, path string, propagation *metav1.DeletionPropagation, grace Grace) error {
	gvr, _ := ctx.Value(internal.KeyGVR).(*client.GVR)
	ns, n := client.Namespaced(path)
	auth, err := w.Client().CanI(ns, gvr, n, []string{client.DeleteVerb})
	if err != nil {
		return err
	}
	if !auth {
		return fmt.Errorf("user is not authorized to delete %s", path)
	}

	var gracePeriod *int64
	if grace != DefaultGrace {
		gracePeriod = (*int64)(&grace)
	}
	opts := metav1.DeleteOptions{
		PropagationPolicy:  propagation,
		GracePeriodSeconds: gracePeriod,
	}

	ctx, cancel := context.WithTimeout(ctx, w.Client().Config().CallTimeout())
	defer cancel()

	d, err := w.Client().DynDial()
	if err != nil {
		return err
	}
	dial := d.Resource(gvr.GVR())
	if client.IsClusterScoped(ns) {
		return dial.Delete(ctx, n, opts)
	}

	return dial.Namespace(ns).Delete(ctx, n, opts)
}

// List fetch workloads from informer caches.
func (a *Workload) List(ctx context.Context, ns string) ([]runtime.Object, error) {
	sel := labels.Everything()
	if labelSel, ok := ctx.Value(internal.KeyLabels).(labels.Selector); ok {
		sel = labelSel
	}

	oo := make([]runtime.Object, 0, 100)
	for _, gvr := range resList {
		objs, err := a.getFactory().List(gvr, ns, true, sel)
		if err != nil {
			return nil, err
		}
		objs, err = filterByFieldSelector(ctx, objs)
		if err != nil {
			return nil, err
		}
		a.appendRows(gvr, objs, &oo)
	}

	return oo, nil
}

// appendRows converts cached objects to workload rows.
func (*Workload) appendRows(gvr *client.GVR, objs []runtime.Object, oo *[]runtime.Object) {
	for _, o := range objs {
		row, ok := wkRow(gvr, o)
		if !ok {
			continue
		}
		*oo = append(*oo, row)
	}
}

// Helpers...

// wkRow renders a cached object as a workload row.
func wkRow(gvr *client.GVR, o runtime.Object) (*render.WorkloadRes, bool) {
	var (
		ns, name, status, ready string
		ts                      metav1.Time
	)
	if gvr == client.PodGVR {
		p, err := client.ToPod(o)
		if err != nil {
			return nil, false
		}
		ns, name, ts = p.Namespace, p.Name, p.CreationTimestamp
		status, ready = wkPodPhase(p)
	} else {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			return nil, false
		}
		ns, name, ts = u.GetNamespace(), u.GetName(), u.GetCreationTimestamp()
		ready = wkReady(gvr, u)
	}
	stat := wkStatus(gvr, status, ready)

	return &render.WorkloadRes{Row: metav1.TableRow{Cells: []any{
		gvr.String(),
		ns,
		name,
		stat,
		wkReadiness(gvr, ready),
		validity(stat),
		ts,
	}}}, true
}

// wkReady renders the ready/desired cell of an unstructured workload.
func wkReady(gvr *client.GVR, u *unstructured.Unstructured) string {
	switch gvr {
	case client.DpGVR, client.StsGVR, client.RsGVR:
		return fmt.Sprintf("%d/%d", wkInt(u, "status", "readyReplicas"), wkInt(u, "spec", "replicas"))
	case client.DsGVR:
		return fmt.Sprintf("%d/%d", wkInt(u, "status", "numberReady"), wkInt(u, "status", "desiredNumberScheduled"))
	}

	return ""
}

// wkInt reads an integer field, defaulting to 0 when unset.
func wkInt(u *unstructured.Unstructured, path ...string) int64 {
	v, found, _ := unstructured.NestedInt64(u.Object, path...)
	if !found {
		return 0
	}

	return v
}

// wkReadiness renders the ready cell, blank for services.
func wkReadiness(gvr *client.GVR, ready string) string {
	if gvr == client.SvcGVR {
		return ""
	}

	return ready
}

// wkStatus computes the workload health from its status and ready cells.
func wkStatus(gvr *client.GVR, status, ready string) string {
	switch gvr {
	case client.PodGVR:
		if status == render.PhaseCompleted {
			return StatusOK
		}
		if !isReady(ready) || status != render.PhaseRunning {
			return DegradedStatus
		}
	case client.DpGVR, client.StsGVR, client.RsGVR, client.DsGVR:
		if !isReady(ready) {
			return DegradedStatus
		}
	case client.SvcGVR:
	default:
		return render.MissingValue
	}

	return StatusOK
}

func validity(status string) string {
	if status != "DEGRADED" {
		return ""
	}

	return status
}

func isReady(s string) bool {
	tt := strings.Split(s, "/")
	if len(tt) != 2 {
		return false
	}
	r, err := strconv.Atoi(tt[0])
	if err != nil {
		slog.Error("Invalid ready count",
			slogs.Error, err,
			slogs.Count, tt[0],
		)
		return false
	}
	c, err := strconv.Atoi(tt[1])
	if err != nil {
		slog.Error("invalid expected count: %q",
			slogs.Error, err,
			slogs.Count, tt[1],
		)
		return false
	}

	if c == 0 {
		return true
	}
	return r == c
}

// ----------------------------------------------------------------------------
// Pod phase helpers (mirror of kubectl's printPod logic)...

const (
	wkNodeUnreachable = "NodeUnreachable"
	wkNotReady        = "NotReady"
	wkUnknown         = "Unknown"
	wkPodInitializing = "PodInitializing"
)

// wkPodPhase renders the pod status and ready cells.
func wkPodPhase(p *v1.Pod) (status, ready string) {
	reason := string(p.Status.Phase)
	if p.Status.Reason != "" {
		reason = p.Status.Reason
	}
	if wkSchedulingGated(p) {
		reason = string(v1.PodReasonSchedulingGated)
	}

	initReason, initializing, readyCount := wkInitPhase(p)
	if initReason != "" {
		reason = initReason
	}
	if !initializing || wkCondTrue(p, v1.PodInitialized) {
		var n int
		reason, n = wkMainPhase(p, reason)
		readyCount += n
	}

	return wkDeletionPhase(p, reason), fmt.Sprintf("%d/%d", readyCount, wkTotalContainers(p))
}

// wkDeletionPhase overrides the status while the pod terminates.
func wkDeletionPhase(p *v1.Pod, reason string) string {
	if p.DeletionTimestamp == nil {
		return reason
	}
	if p.Status.Reason == wkNodeUnreachable {
		return wkUnknown
	}
	if !wkPhaseTerminal(p.Status.Phase) {
		return render.PhaseTerminating
	}

	return reason
}

// wkSchedulingGated reports whether the pod is scheduling gated.
func wkSchedulingGated(p *v1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == v1.PodScheduled && c.Reason == string(v1.PodReasonSchedulingGated) {
			return true
		}
	}

	return false
}

// wkInitPhase walks init containers, returning the status override,
// whether initialization is stuck, and the ready container count.
func wkInitPhase(p *v1.Pod) (reason string, initializing bool, ready int) {
	restartable := wkRestartableInit(p)
	for i := range p.Status.InitContainerStatuses {
		cs := &p.Status.InitContainerStatuses[i]
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue
		}
		if restartable[cs.Name] && cs.Started != nil && *cs.Started {
			if cs.Ready {
				ready++
			}
			continue
		}
		return wkInitReason(p, cs, i), true, ready
	}

	return "", false, ready
}

// wkInitReason renders the status of a stuck init container.
func wkInitReason(p *v1.Pod, cs *v1.ContainerStatus, i int) string {
	switch {
	case cs.State.Terminated != nil:
		t := cs.State.Terminated
		if t.Reason == "" {
			if t.Signal != 0 {
				return fmt.Sprintf("Init:Signal:%d", t.Signal)
			}
			return fmt.Sprintf("Init:ExitCode:%d", t.ExitCode)
		}
		return "Init:" + t.Reason
	case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != wkPodInitializing:
		return "Init:" + cs.State.Waiting.Reason
	default:
		return fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
	}
}

// wkMainPhase walks app containers in reverse, returning the status override
// and the ready container count.
func wkMainPhase(p *v1.Pod, status string) (reason string, ready int) {
	reason = status
	hasRunning, errorReason := false, ""
	for i := len(p.Status.ContainerStatuses) - 1; i >= 0; i-- {
		cs := &p.Status.ContainerStatuses[i]
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
			reason = cs.State.Waiting.Reason
		case cs.State.Terminated != nil:
			reason = wkTermReason(cs.State.Terminated)
			if cs.State.Terminated.ExitCode != 0 {
				errorReason = reason
			}
		case cs.Ready && cs.State.Running != nil:
			hasRunning = true
			ready++
		}
	}

	return wkFixCompleted(p, reason, hasRunning, errorReason), ready
}

// wkFixCompleted rewrites a Completed status when containers still run.
func wkFixCompleted(p *v1.Pod, reason string, hasRunning bool, errorReason string) string {
	if reason != render.PhaseCompleted {
		return reason
	}
	switch {
	case hasRunning && wkCondTrue(p, v1.PodReady):
		return render.PhaseRunning
	case errorReason != "":
		return errorReason
	case hasRunning:
		return wkNotReady
	}

	return reason
}

// wkTermReason renders a terminated container status.
func wkTermReason(t *v1.ContainerStateTerminated) string {
	switch {
	case t.Reason != "":
		return t.Reason
	case t.Signal != 0:
		return fmt.Sprintf("Signal:%d", t.Signal)
	default:
		return fmt.Sprintf("ExitCode:%d", t.ExitCode)
	}
}

// wkTotalContainers tallies containers plus restartable init containers.
func wkTotalContainers(p *v1.Pod) int {
	total := len(p.Spec.Containers)
	for i := range p.Spec.InitContainers {
		if wkRestartable(&p.Spec.InitContainers[i]) {
			total++
		}
	}

	return total
}

// wkRestartableInit maps init container names with an Always restart policy.
func wkRestartableInit(p *v1.Pod) map[string]bool {
	mm := make(map[string]bool, len(p.Spec.InitContainers))
	for i := range p.Spec.InitContainers {
		if wkRestartable(&p.Spec.InitContainers[i]) {
			mm[p.Spec.InitContainers[i].Name] = true
		}
	}

	return mm
}

// wkRestartable reports whether the container restarts on failure.
func wkRestartable(c *v1.Container) bool {
	return c.RestartPolicy != nil && *c.RestartPolicy == v1.ContainerRestartPolicyAlways
}

// wkCondTrue checks for a true pod condition.
func wkCondTrue(p *v1.Pod, t v1.PodConditionType) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == t && c.Status == v1.ConditionTrue {
			return true
		}
	}

	return false
}

// wkPhaseTerminal reports whether the pod phase is terminal.
func wkPhaseTerminal(phase v1.PodPhase) bool {
	return phase == v1.PodSucceeded || phase == v1.PodFailed
}

// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"strings"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/duration"
)

var evColumns = []metav1.TableColumnDefinition{
	{Name: "Last Seen", Type: colTypeStr},
	{Name: "Type", Type: colTypeStr},
	{Name: "Reason", Type: colTypeStr},
	{Name: "Object", Type: colTypeStr},
	{Name: "Subobject", Type: colTypeStr, Priority: 1},
	{Name: "Source", Type: colTypeStr, Priority: 1},
	{Name: "Message", Type: colTypeStr},
	{Name: "First Seen", Type: colTypeStr, Priority: 1},
	{Name: "Count", Type: colTypeStr, Priority: 1},
	{Name: "Name", Type: colTypeStr, Priority: 1, Format: "name"},
}

// Event retrieves events from the informer cache as a k8s table.
type Event struct {
	Resource
}

// List returns a collection of events.
func (e *Event) List(ctx context.Context, ns string) ([]runtime.Object, error) {
	sel := labels.Everything()
	if labelSel, ok := ctx.Value(internal.KeyLabels).(labels.Selector); ok {
		sel = labelSel
	}
	oo, err := e.getFactory().List(client.EvGVR, ns, true, sel)
	if err != nil {
		return nil, err
	}
	oo, err = filterByFieldSelector(ctx, oo)
	if err != nil {
		return nil, err
	}

	table := &metav1.Table{
		ColumnDefinitions: append([]metav1.TableColumnDefinition{{Name: nsColName, Type: colTypeStr}}, evColumns...),
	}
	for _, o := range oo {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		row := evRow(u, e.includeObj)
		row.Cells = append([]any{u.GetNamespace()}, row.Cells...)
		table.Rows = append(table.Rows, row)
	}

	return []runtime.Object{table}, nil
}

// ----------------------------------------------------------------------------
// Helpers (mirror of kubectl's printEvent logic)...

// evRow renders a cached event as a table row.
func evRow(u *unstructured.Unstructured, includeObj bool) metav1.TableRow {
	row := metav1.TableRow{}
	if includeObj {
		row.Object.Object = u
	} else {
		row.Object.Object = evMeta(u)
	}

	firstSeen, zero := evAge(u, "deprecatedFirstTimestamp")
	if zero {
		firstSeen, _ = evAge(u, "eventTime")
	}
	lastSeen, zero := evAge(u, "deprecatedLastTimestamp")
	if zero {
		lastSeen = firstSeen
	}
	count := evInt(u, "deprecatedCount")
	if sm, found, _ := unstructured.NestedMap(u.Object, "series"); found && sm != nil {
		lastSeen, _ = evAge(u, "series", "lastObservedTime")
		count = evInt(u, "series", "count")
	} else if count == 0 {
		count = 1
	}

	row.Cells = append(row.Cells,
		lastSeen,
		evStr(u, "type"),
		evStr(u, "reason"),
		evTarget(u),
		evStr(u, "regarding", "fieldPath"),
		evSource(u),
		strings.TrimSpace(evStr(u, "note")),
		firstSeen,
		count,
		u.GetName(),
	)

	return row
}

// evMeta extracts a partial object metadata payload for the row.
func evMeta(u *unstructured.Unstructured) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "meta.k8s.io/v1",
		"kind":       "PartialObjectMetadata",
		"metadata": map[string]any{
			"name":              u.GetName(),
			"namespace":         u.GetNamespace(),
			"uid":               string(u.GetUID()),
			"resourceVersion":   u.GetResourceVersion(),
			"creationTimestamp": evStr(u, "metadata", "creationTimestamp"),
		},
	}}
}

// evTarget renders the involved object reference.
func evTarget(u *unstructured.Unstructured) string {
	kind := strings.ToLower(evStr(u, "regarding", "kind"))
	name := evStr(u, "regarding", "name")
	if name == "" {
		return kind
	}

	return kind + "/" + name
}

// evSource renders the event source, legacy fields first.
func evSource(u *unstructured.Unstructured) string {
	component := evFirst(evStr(u, "deprecatedSource", "component"), evStr(u, "reportingController"))
	instance := evFirst(evStr(u, "deprecatedSource", "host"), evStr(u, "reportingInstance"))
	if instance == "" {
		return component
	}

	return component + ", " + instance
}

// evFirst returns the first non-empty string.
func evFirst(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}

	return ""
}

// evAge renders a timestamp field as a human age, reporting whether it is unset.
func evAge(u *unstructured.Unstructured, path ...string) (string, bool) {
	s, _, _ := unstructured.NestedString(u.Object, path...)
	if s == "" {
		return render.UnknownValue, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return render.UnknownValue, false
	}
	if t.IsZero() {
		return render.UnknownValue, true
	}

	return duration.HumanDuration(time.Since(t)), false
}

// evInt reads an integer field, defaulting to 0.
func evInt(u *unstructured.Unstructured, path ...string) int64 {
	v, found, _ := unstructured.NestedInt64(u.Object, path...)
	if !found {
		return 0
	}

	return v
}

// evStr reads a string field.
func evStr(u *unstructured.Unstructured, path ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, path...)

	return s
}

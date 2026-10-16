// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model1

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/fvbommel/sortorder"
)

type ReRangeFn func(int, RowEvent) bool

// ResEvent represents a resource event.
type ResEvent int

// RowEvent tracks resource instance events.
type RowEvent struct {
	Kind   ResEvent
	Row    Row
	Deltas DeltaRow
}

// NewRowEvent returns a new row event.
func NewRowEvent(kind ResEvent, row Row) RowEvent {
	return RowEvent{
		Kind: kind,
		Row:  row,
	}
}

// NewRowEventWithDeltas returns a new row event with deltas.
func NewRowEventWithDeltas(row Row, delta DeltaRow) RowEvent {
	return RowEvent{
		Kind:   EventUpdate,
		Row:    row,
		Deltas: delta,
	}
}

// Clone returns a row event deep copy.
func (r RowEvent) Clone() RowEvent {
	return RowEvent{
		Kind:   r.Kind,
		Row:    r.Row.Clone(),
		Deltas: r.Deltas.Clone(),
	}
}

// Customize returns a new subset based on the given column indices.
func (r RowEvent) Customize(cols []int) RowEvent {
	delta := r.Deltas
	if !r.Deltas.IsBlank() {
		delta = make(DeltaRow, len(cols))
		r.Deltas.Customize(cols, delta)
	}

	return RowEvent{
		Kind:   r.Kind,
		Deltas: delta,
		Row:    r.Row.Customize(cols),
	}
}

// ExtractHeaderLabels extract collection of fields into header.
func (r RowEvent) ExtractHeaderLabels(labelCol int) []string {
	hh, _ := sortLabels(labelize(r.Row.Fields[labelCol]))
	return hh
}

// Labelize returns a new row event based on labels.
func (r RowEvent) Labelize(cols []int, labelCol int, labels []string) RowEvent {
	return RowEvent{
		Kind:   r.Kind,
		Deltas: r.Deltas.Labelize(cols, labelCol),
		Row:    r.Row.Labelize(cols, labelCol, labels),
	}
}

// Diff returns true if the row changed.
func (r RowEvent) Diff(re RowEvent, ageCol int) bool {
	if r.Kind != re.Kind {
		return true
	}
	if r.Deltas.Diff(re.Deltas, ageCol) {
		return true
	}
	return r.Row.Diff(re.Row, ageCol)
}

// ----------------------------------------------------------------------------

type reIndex map[string]int

// RowEvents a collection of row events.
type RowEvents struct {
	events []RowEvent
	index  reIndex
}

func NewRowEvents(size int) *RowEvents {
	return &RowEvents{
		events: make([]RowEvent, 0, size),
		index:  make(reIndex, size),
	}
}

func NewRowEventsWithEvts(ee ...RowEvent) *RowEvents {
	re := NewRowEvents(len(ee))
	for _, e := range ee {
		re.Add(e)
	}

	return re
}

func (r *RowEvents) reindex() {
	for i, e := range r.events {
		r.index[e.Row.ID] = i
	}
}

func (r *RowEvents) At(i int) (RowEvent, bool) {
	if i < 0 || i > len(r.events) {
		return RowEvent{}, false
	}

	return r.events[i], true
}

func (r *RowEvents) Set(i int, re RowEvent) {
	r.events[i] = re
	r.index[re.Row.ID] = i
}

func (r *RowEvents) Add(re RowEvent) {
	r.events = append(r.events, re)
	r.index[re.Row.ID] = len(r.events) - 1
}

// ExtractHeaderLabels extract header labels.
func (r *RowEvents) ExtractHeaderLabels(labelCol int) []string {
	ll := make([]string, 0, 10)
	for _, re := range r.events {
		ll = append(ll, re.ExtractHeaderLabels(labelCol)...)
	}

	return ll
}

// Labelize converts labels into a row event.
func (r *RowEvents) Labelize(cols []int, labelCol int, labels []string) *RowEvents {
	out := make([]RowEvent, 0, len(r.events))
	for _, re := range r.events {
		out = append(out, re.Labelize(cols, labelCol, labels))
	}

	return NewRowEventsWithEvts(out...)
}

// Customize returns custom row events based on columns layout.
func (r *RowEvents) Customize(cols []int) *RowEvents {
	ee := make([]RowEvent, 0, len(cols))
	for _, re := range r.events {
		ee = append(ee, re.Customize(cols))
	}

	return NewRowEventsWithEvts(ee...)
}

// Diff returns true if the event changed.
func (r *RowEvents) Diff(re *RowEvents, ageCol int) bool {
	if len(r.events) != len(re.events) {
		return true
	}
	for i := range r.events {
		if r.events[i].Diff(re.events[i], ageCol) {
			return true
		}
	}

	return false
}

// Clone returns a deep copy.
func (r *RowEvents) Clone() *RowEvents {
	re := make([]RowEvent, 0, len(r.events))
	for _, e := range r.events {
		re = append(re, e.Clone())
	}

	return NewRowEventsWithEvts(re...)
}

// Upsert add or update a row if it exists.
func (r *RowEvents) Upsert(re RowEvent) {
	if idx, ok := r.FindIndex(re.Row.ID); ok {
		r.events[idx] = re
	} else {
		r.Add(re)
	}
}

// DeleteBatch removes multiple elements by id with a single reindex.
func (r *RowEvents) DeleteBatch(fqns []string) {
	victims := make(map[int]struct{}, len(fqns))
	for _, fqn := range fqns {
		if idx, ok := r.FindIndex(fqn); ok {
			victims[idx] = struct{}{}
			delete(r.index, fqn)
		}
	}
	if len(victims) == 0 {
		return
	}
	filtered := make([]RowEvent, 0, len(r.events)-len(victims))
	for i, e := range r.events {
		if _, ok := victims[i]; !ok {
			filtered = append(filtered, e)
		}
	}
	r.events = filtered
	r.reindex()
}

func (r *RowEvents) Len() int {
	return len(r.events)
}

func (r *RowEvents) Empty() bool {
	return len(r.events) == 0
}

// Clear delete all row events.
func (r *RowEvents) Clear() {
	r.events = r.events[:0]
	for k := range r.index {
		delete(r.index, k)
	}
}

func (r *RowEvents) Range(f ReRangeFn) {
	for i, e := range r.events {
		if !f(i, e) {
			return
		}
	}
}

func (r *RowEvents) Get(id string) (RowEvent, bool) {
	i, ok := r.index[id]
	if !ok {
		return RowEvent{}, false
	}

	return r.At(i)
}

// FindIndex locates a row index by id. Returns false is not found.
func (r *RowEvents) FindIndex(id string) (int, bool) {
	i, ok := r.index[id]

	return i, ok
}

// Sort rows based on column index and order.
func (r *RowEvents) Sort(_ string, sortCol int, isDuration, numCol, isCapacity, asc bool) {
	if sortCol == -1 || r == nil {
		return
	}

	// Parse each row's sort key once instead of on every comparison.
	kk := make([]rowSortKey, len(r.events))
	for i := range r.events {
		kk[i].id = r.events[i].Row.ID
		v := r.events[i].Row.Fields[sortCol]
		switch {
		case numCol:
			kk[i].raw, kk[i].num = v, strings.ReplaceAll(v, ",", "")
		case isDuration:
			kk[i].n = durationToSeconds(v)
		case isCapacity:
			kk[i].n = capacityToNumber(v)
		default:
			kk[i].raw = v
		}
	}
	sort.Sort(rowSorter{events: r.events, keys: kk, isNumber: numCol, isDuration: isDuration, isCapacity: isCapacity, asc: asc})
	r.reindex()
}

// For debugging...
func (re RowEvents) Dump(msg string) {
	slog.Debug("[DEBUG] RowEvents" + msg)
	for _, r := range re.events {
		slog.Debug(fmt.Sprintf("   %#v", r))
	}
}

// ----------------------------------------------------------------------------
// Sorting...

// rowSortKey caches a row's parsed sort value so a sort compares keys
// instead of reparsing fields on every comparison.
type rowSortKey struct {
	id  string
	raw string
	num string
	n   int64
}

// rowSorter sorts row events against precomputed sort keys, swapping events
// and keys in lockstep. Comparisons keep Less semantics, incl. the id
// tie-break on equal values and ascending/descending order.
type rowSorter struct {
	events     []RowEvent
	keys       []rowSortKey
	isNumber   bool
	isDuration bool
	isCapacity bool
	asc        bool
}

func (s rowSorter) Len() int { return len(s.events) }

func (s rowSorter) Swap(i, j int) {
	s.events[i], s.events[j] = s.events[j], s.events[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}

func (s rowSorter) Less(i, j int) bool {
	k1, k2 := &s.keys[i], &s.keys[j]

	var less, tie bool
	switch {
	case s.isNumber:
		less, tie = sortorder.NaturalLess(k1.num, k2.num), k1.raw == k2.raw
	case s.isDuration, s.isCapacity:
		less, tie = k1.n < k2.n, k1.n == k2.n
	default:
		less, tie = sortorder.NaturalLess(k1.raw, k2.raw), k1.raw == k2.raw
	}
	if tie {
		less = sortorder.NaturalLess(k1.id, k2.id)
	}
	if !s.asc {
		return !less
	}

	return less
}

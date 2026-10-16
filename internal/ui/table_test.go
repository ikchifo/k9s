// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/ui"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTableNew(t *testing.T) {
	v := ui.NewTable(client.NewGVR("fred"))
	v.Init(makeContext())

	assert.Equal(t, "fred", v.GVR().String())
}

func TestTableUpdate(t *testing.T) {
	v := ui.NewTable(client.NewGVR("fred"))
	v.Init(makeContext())

	data := makeTableData()
	cdata := v.Update(data, false)
	v.UpdateUI(cdata, data)

	assert.Equal(t, data.RowCount()+1, v.GetRowCount())
	assert.Equal(t, data.HeaderCount(), v.GetColumnCount())
}

func TestTableSelection(t *testing.T) {
	v := ui.NewTable(client.NewGVR("fred"))
	v.Init(makeContext())
	m := new(mockModel)
	v.SetModel(m)
	data := m.Peek()
	cdata := v.Update(data, false)
	v.UpdateUI(cdata, data)
	v.SelectRow(1, 0, true)

	r := v.GetSelectedRow("r1")
	if r != nil {
		assert.Equal(t, model1.Row{ID: "r1", Fields: model1.Fields{"blee", "duh", "fred"}}, *r)
	}
	assert.Equal(t, "r1", v.GetSelectedItem())
	assert.Equal(t, "blee", v.GetSelectedCell(0))
	assert.Equal(t, 1, v.GetSelectedRowIndex())
	assert.Equal(t, []string{"r1"}, v.GetSelectedItems())

	v.ClearSelection()
	v.SelectFirstRow()
	assert.Equal(t, 1, v.GetSelectedRowIndex())
}

// ----------------------------------------------------------------------------
// Helpers...

type mockModel struct{}

var _ ui.Tabular = &mockModel{}

func (*mockModel) SetViewSetting(context.Context, *config.ViewSetting) {}
func (*mockModel) SetInstance(string)                                  {}
func (*mockModel) SetLabelSelector(labels.Selector)                    {}
func (*mockModel) GetLabelSelector() labels.Selector                   { return nil }
func (*mockModel) Empty() bool                                         { return false }
func (*mockModel) RowCount() int                                       { return 1 }
func (*mockModel) HasMetrics() bool                                    { return true }
func (*mockModel) Peek() *model1.TableData                             { return makeTableData() }
func (*mockModel) Refresh(context.Context) error                       { return nil }
func (*mockModel) ClusterWide() bool                                   { return false }
func (*mockModel) GetNamespace() string                                { return "blee" }
func (*mockModel) SetNamespace(string)                                 {}
func (*mockModel) ToggleToast()                                        {}
func (*mockModel) AddListener(model.TableListener)                     {}
func (*mockModel) RemoveListener(model.TableListener)                  {}
func (*mockModel) Watch(context.Context) error                         { return nil }
func (*mockModel) Get(context.Context, string) (runtime.Object, error) { return nil, nil }
func (*mockModel) InNamespace(string) bool                             { return true }
func (*mockModel) SetRefreshRate(time.Duration)                        {}

func (*mockModel) Delete(context.Context, string, *metav1.DeletionPropagation, dao.Grace) error {
	return nil
}

func (*mockModel) Describe(context.Context, string) (string, error) {
	return "", nil
}

func (*mockModel) ToYAML(context.Context, string) (string, error) {
	return "", nil
}

func makeTableData() *model1.TableData {
	return model1.NewTableDataWithRows(
		client.NewGVR("test"),
		model1.Header{
			model1.HeaderColumn{Name: "A"},
			model1.HeaderColumn{Name: "B"},
			model1.HeaderColumn{Name: "C"},
		},
		model1.NewRowEventsWithEvts(
			model1.RowEvent{
				Row: model1.Row{
					ID:     "r1",
					Fields: model1.Fields{"blee", "duh", "fred"},
				},
			},
			model1.RowEvent{
				Row: model1.Row{
					ID:     "r2",
					Fields: model1.Fields{"blee", "duh", "zorg"},
				},
			},
		),
	)
}

func makeContext() context.Context {
	ctx := context.WithValue(context.Background(), internal.KeyStyles, config.NewStyles())
	ctx = context.WithValue(ctx, internal.KeyViewConfig, config.NewCustomView())

	return ctx
}

func TestTableUpdateReusesCells(t *testing.T) {
	uu := map[string][]*model1.TableData{
		"shrink":      {makeRows(3, 3), makeRows(1, 3)},
		"grow":        {makeRows(1, 3), makeRows(3, 3)},
		"fewer cols":  {makeRows(2, 3), makeRows(2, 2)},
		"short rows":  {makeRows(2, 3), withShortRow(makeRows(2, 3))},
		"same shape":  {makeRows(3, 3), makeRows(3, 3)},
		"emptied out": {makeRows(3, 3), makeRows(0, 3)},
	}

	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			v := ui.NewTable(client.NewGVR("fred"))
			v.Init(makeContext())
			for _, d := range u {
				v.UpdateUI(v.Update(d, false), d)
			}
			v.ShowDeleted()
			last := u[len(u)-1]
			v.UpdateUI(v.Update(last, false), last)

			e := ui.NewTable(client.NewGVR("fred"))
			e.Init(makeContext())
			e.UpdateUI(e.Update(last, false), last)

			assert.Equal(t, e.GetRowCount(), v.GetRowCount())
			assert.Equal(t, e.GetColumnCount(), v.GetColumnCount())
			for r := range e.GetRowCount() {
				for c := range e.GetColumnCount() {
					assert.Equal(t, *e.GetCell(r, c), *v.GetCell(r, c), "cell %d,%d", r, c)
				}
			}
		})
	}
}

func makeRows(n, cols int) *model1.TableData {
	h := make(model1.Header, cols)
	for c := range h {
		h[c] = model1.HeaderColumn{Name: fmt.Sprintf("C%d", c)}
	}
	ee := make([]model1.RowEvent, 0, n)
	for r := range n {
		ff := make(model1.Fields, cols)
		for c := range ff {
			ff[c] = fmt.Sprintf("r%d-c%d", r, c)
		}
		ee = append(ee, model1.RowEvent{Kind: model1.ResEvent(r % 3), Row: model1.Row{ID: fmt.Sprintf("r%d", r), Fields: ff}})
	}

	return model1.NewTableDataWithRows(client.NewGVR("test"), h, model1.NewRowEventsWithEvts(ee...))
}

func withShortRow(d *model1.TableData) *model1.TableData {
	re, _ := d.RowAt(0)
	re.Row.Fields = re.Row.Fields[:1]
	d.SetRow(0, re)

	return d
}

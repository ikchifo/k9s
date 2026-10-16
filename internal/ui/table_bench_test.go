// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui_test

import (
	"fmt"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/ui"
)

const benchRows = 20_000

type peekModel struct {
	*model.Table
	data *model1.TableData
}

func (m *peekModel) Peek() *model1.TableData { return m.data.Clone() }
func (m *peekModel) RowCount() int           { return m.data.RowCount() }
func (m *peekModel) Empty() bool             { return m.data.Empty() }

func benchPods(n int) *model1.TableData {
	h := render.NewPod().Header(client.NamespaceAll)
	ss := []string{render.PhaseRunning, render.PhasePending, render.PhaseCompleted, "CrashLoopBackOff"}
	ee := make([]model1.RowEvent, 0, n)
	for i := range n {
		ns, name := fmt.Sprintf("team-%02d", i%20), fmt.Sprintf("svc-%d-7f9c49b5c8-%05d", i%50, i)
		ff := make(model1.Fields, len(h))
		for c := range ff {
			ff[c] = fmt.Sprintf("v%d", (i+c)%97)
		}
		ff[0], ff[1], ff[5], ff[6] = ns, name, ss[i%len(ss)], fmt.Sprint(i%7)
		ff[len(ff)-2], ff[len(ff)-1] = "", fmt.Sprintf("%dd", i%300)
		re := model1.NewRowEvent(model1.ResEvent(i%3), model1.Row{ID: ns + "/" + name, Fields: ff})
		if i%10 == 0 {
			re.Deltas = make(model1.DeltaRow, len(h))
			re.Deltas[6] = "0"
		}
		ee = append(ee, re)
	}

	return model1.NewTableDataFull(client.PodGVR, client.NamespaceAll, h, model1.NewRowEventsWithEvts(ee...))
}

func benchTable(b *testing.B) *ui.Table {
	m := &peekModel{Table: model.NewTable(client.PodGVR), data: benchPods(benchRows)}
	m.SetNamespace(client.NamespaceAll)
	t := ui.NewTable(client.PodGVR)
	t.SetModel(m)
	t.Init(makeContext())
	t.SetColorerFn(render.NewPod().ColorerFunc())
	t.SetSortCol("NAMESPACE", true)
	t.Refresh()
	b.ReportAllocs()

	return t
}

func BenchmarkTableRefresh(b *testing.B) {
	t := benchTable(b)
	for b.Loop() {
		t.Refresh()
	}
}

func BenchmarkTableSortToggle(b *testing.B) {
	t := benchTable(b)
	sort := t.SortColCmd("AGE", true)
	for b.Loop() {
		sort(nil)
	}
}

func BenchmarkTableSelectedRow(b *testing.B) {
	t := benchTable(b)
	t.SelectRow(benchRows/2, 0, true)
	for b.Loop() {
		if t.GetSelectedRow(t.GetSelectedItem()) == nil {
			b.Fatal("no selection")
		}
	}
}

func BenchmarkTableColumnMove(b *testing.B) {
	t := benchTable(b)
	for b.Loop() {
		t.SelectNextColumn()
	}
}

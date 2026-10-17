// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/watch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
)

func TestTableReconcile(t *testing.T) {
	ta := NewTable(client.PodGVR)
	ta.SetNamespace(client.NamespaceAll)

	f := makeFactory()
	f.rows = []runtime.Object{load(t, "p1")}
	ctx := context.WithValue(context.Background(), internal.KeyFactory, f)
	ctx = context.WithValue(ctx, internal.KeyFields, "")
	ctx = context.WithValue(ctx, internal.KeyWithMetrics, false)
	_, err := ta.reconcile(ctx)
	require.NoError(t, err)
	data := ta.Peek()
	assert.Equal(t, 26, data.HeaderCount())
	assert.Equal(t, 1, data.RowCount())
	assert.Equal(t, client.NamespaceAll, data.GetNamespace())
}

func TestTableList(t *testing.T) {
	ta := NewTable(client.PodGVR)
	ta.SetNamespace("blee")

	acc := accessor{}
	ctx := context.WithValue(context.Background(), internal.KeyFactory, makeFactory())
	rows, err := ta.list(ctx, &acc)
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestTableGet(t *testing.T) {
	ta := NewTable(client.PodGVR)
	ta.SetNamespace("blee")

	f := makeFactory()
	f.rows = []runtime.Object{load(t, "p1")}
	ctx := context.WithValue(context.Background(), internal.KeyFactory, f)
	ctx = context.WithValue(ctx, internal.KeyWithMetrics, false)
	row, err := ta.Get(ctx, "fred")
	require.NoError(t, err)
	assert.NotNil(t, row)
	assert.Equal(t, "nginx-7fb78fb6d8-2w75j", row.(*render.PodWithMetrics).Raw.Name)
}

func TestTableMeta(t *testing.T) {
	uu := map[string]struct {
		gvr      *client.GVR
		accessor dao.Accessor
		renderer model1.Renderer
	}{
		"generic": {
			gvr:      client.CoGVR,
			accessor: &dao.Container{},
			renderer: &render.Container{},
		},
		"node": {
			gvr:      client.NodeGVR,
			accessor: &dao.Node{},
			renderer: &render.Node{},
		},
	}

	for k := range uu {
		u := uu[k]
		ta := NewTable(u.gvr)
		m := resourceMeta(ta.gvr)

		assert.Equal(t, u.accessor, m.DAO)
		assert.Equal(t, u.renderer, m.Renderer)
	}
}

// ----------------------------------------------------------------------------
// Helpers...

func mustLoad(n string) *v1.Pod {
	raw, err := os.ReadFile(fmt.Sprintf("testdata/%s.json", n))
	if err != nil {
		panic(err)
	}
	var o unstructured.Unstructured
	if err = json.Unmarshal(raw, &o); err != nil {
		panic(err)
	}
	var pod v1.Pod
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &pod); err != nil {
		panic(err)
	}
	return &pod
}

func load(t *testing.T, n string) *v1.Pod {
	raw, err := os.ReadFile(fmt.Sprintf("testdata/%s.json", n))
	require.NoError(t, err)
	var o unstructured.Unstructured
	err = json.Unmarshal(raw, &o)
	require.NoError(t, err)
	var pod v1.Pod
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(o.Object, &pod))
	return &pod
}

// ----------------------------------------------------------------------------

func makeFactory() testFactory {
	return testFactory{}
}

type testFactory struct {
	rows []runtime.Object
}

var _ dao.Factory = testFactory{}

func (testFactory) Client() client.Connection {
	return client.NewTestAPIClient()
}

func (f testFactory) Get(*client.GVR, string, bool, labels.Selector) (runtime.Object, error) {
	if len(f.rows) > 0 {
		return f.rows[0], nil
	}
	return nil, nil
}

func (f testFactory) List(*client.GVR, string, bool, labels.Selector) ([]runtime.Object, error) {
	if len(f.rows) > 0 {
		return f.rows, nil
	}
	return nil, nil
}

func (testFactory) ForResource(string, *client.GVR) (informers.GenericInformer, error) {
	return nil, nil
}

func (testFactory) CanForResource(string, *client.GVR, []string) (informers.GenericInformer, error) {
	return nil, nil
}
func (testFactory) WaitForCacheSync() {}
func (testFactory) Forwarders() watch.Forwarders {
	return nil
}
func (testFactory) DeleteForwarder(string) {}

// ----------------------------------------------------------------------------

type accessor struct {
	gvr *client.GVR
}

var _ dao.Accessor = (*accessor)(nil)

func (*accessor) SetIncludeObject(bool) {}

func (*accessor) List(context.Context, string) ([]runtime.Object, error) {
	return []runtime.Object{&render.PodWithMetrics{Raw: mustLoad("p1")}}, nil
}

func (*accessor) Get(context.Context, string) (runtime.Object, error) {
	return &render.PodWithMetrics{Raw: mustLoad("p1")}, nil
}

func (a *accessor) Init(_ dao.Factory, gvr *client.GVR) {
	a.gvr = gvr
}

func (a *accessor) GVR() string {
	return a.gvr.String()
}

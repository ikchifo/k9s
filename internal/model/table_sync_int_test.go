// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package model

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

const syncDeadline = initRefreshRate / 2

func TestTableWatchRefreshesOnSync(t *testing.T) {
	_, f, rows := watchSyncTable(t)

	close(f.synced)

	assert.Equal(t, 1, waitRows(t, rows))
	assert.Equal(t, 1, f.lists())
}

func TestTableWatchSyncWaitsInFlightRefresh(t *testing.T) {
	ta, f, rows := watchSyncTable(t)

	ta.updateMx.Lock()
	close(f.synced)
	time.Sleep(syncDeadline / 4)
	ta.updateMx.Unlock()

	assert.Equal(t, 1, waitRows(t, rows))
}

func TestTableWatchSyncedSkipsChannel(t *testing.T) {
	f := newSyncFactory(t)
	close(f.synced)

	ta := NewTable(client.PodGVR)
	ta.SetNamespace(client.NamespaceAll)

	assert.Nil(t, ta.syncedChan(syncContext(t, f)))
}

func watchSyncTable(t *testing.T) (*Table, *syncFactory, chan int) {
	t.Helper()

	f := newSyncFactory(t)
	ta := NewTable(client.PodGVR)
	ta.SetNamespace(client.NamespaceAll)
	rows := make(chan int, 10)
	ta.AddListener(&syncListener{rows: rows})

	ctx := syncContext(t, f)
	assert.NotNil(t, ta.syncedChan(ctx))
	require.NoError(t, ta.Watch(ctx))
	assert.Equal(t, 0, <-rows)

	return ta, f, rows
}

func waitRows(t *testing.T, rows <-chan int) int {
	t.Helper()

	start := time.Now()
	select {
	case n := <-rows:
		t.Logf("refreshed %v after sync", time.Since(start))
		return n
	case <-time.After(syncDeadline):
		t.Fatalf("no refresh within %v of sync", syncDeadline)
		return -1
	}
}

func syncContext(t *testing.T, f *syncFactory) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = context.WithValue(ctx, internal.KeyFactory, f)
	ctx = context.WithValue(ctx, internal.KeyFields, "")

	return context.WithValue(ctx, internal.KeyWithMetrics, false)
}

// ----------------------------------------------------------------------------

type syncListener struct {
	rows chan<- int
}

func (l *syncListener) TableNoData(d *model1.TableData)      { l.rows <- d.RowCount() }
func (l *syncListener) TableDataChanged(d *model1.TableData) { l.rows <- d.RowCount() }
func (*syncListener) TableLoadFailed(error)                  {}

type syncFactory struct {
	testFactory
	t      *testing.T
	inf    *syncInformer
	synced chan struct{}
	listed chan struct{}
}

func newSyncFactory(t *testing.T) *syncFactory {
	dao.MetaAccess.RegisterMeta(client.PodGVR.String(), &metav1.APIResource{Name: "pods", Namespaced: true})
	synced := make(chan struct{})

	return &syncFactory{
		t:      t,
		inf:    &syncInformer{synced: synced},
		synced: synced,
		listed: make(chan struct{}, 10),
	}
}

func (f *syncFactory) lists() int {
	return len(f.listed)
}

func (f *syncFactory) List(*client.GVR, string, bool, labels.Selector) ([]runtime.Object, error) {
	select {
	case <-f.synced:
		f.listed <- struct{}{}
		return []runtime.Object{load(f.t, "p1")}, nil
	default:
		return nil, nil
	}
}

func (f *syncFactory) CanForResource(string, *client.GVR, []string) (informers.GenericInformer, error) {
	return f.inf, nil
}

type syncInformer struct {
	cache.SharedIndexInformer
	synced chan struct{}
}

func (i *syncInformer) Informer() cache.SharedIndexInformer { return i }
func (*syncInformer) Lister() cache.GenericLister           { return nil }
func (i *syncInformer) HasSyncedChecker() cache.DoneChecker { return i }
func (*syncInformer) Name() string                          { return "sync" }
func (i *syncInformer) Done() <-chan struct{}               { return i.synced }

func (i *syncInformer) HasSynced() bool {
	return cache.IsDone(i)
}

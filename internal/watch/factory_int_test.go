// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package watch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

const (
	syncPods  = 5_000
	syncDelay = 20 * time.Millisecond
)

func TestWaitForSyncSeesFullStore(t *testing.T) {
	inf := newSyncInformer(t, syncDelay)

	start := time.Now()
	waitForSync(inf)
	elapsed := time.Since(start)

	oo, err := inf.Lister().List(labels.Everything())
	require.NoError(t, err)
	assert.Len(t, oo, syncPods)
	assert.Less(t, elapsed, defaultWaitTime)
	t.Logf("synced %d pods in %v", syncPods, elapsed)
}

func TestWaitForSyncTimesOut(t *testing.T) {
	inf := newSyncInformer(t, time.Hour)

	start := time.Now()
	waitForSync(inf)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, defaultWaitTime)
	assert.False(t, inf.Informer().HasSynced())
	t.Logf("gave up after %v", elapsed)
}

// ----------------------------------------------------------------------------

type syncInformer struct {
	inf cache.SharedIndexInformer
}

func (i syncInformer) Informer() cache.SharedIndexInformer { return i.inf }

func (i syncInformer) Lister() cache.GenericLister {
	return cache.NewGenericLister(i.inf.GetIndexer(), schema.GroupResource{Resource: "pods"})
}

type listWatch struct {
	*cache.ListWatch
}

func (listWatch) IsWatchListSemanticsUnSupported() bool { return true }

func newSyncInformer(t *testing.T, delay time.Duration) syncInformer {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	lw := listWatch{&cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, _ metav1.ListOptions) (runtime.Object, error) {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return podList(), nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			return watch.NewFake(), nil
		},
	}}
	inf := cache.NewSharedIndexInformer(lw, &unstructured.Unstructured{}, 0, cache.Indexers{})
	done := make(chan struct{})
	go func() {
		inf.RunWithContext(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return syncInformer{inf: inf}
}

func podList() *unstructured.UnstructuredList {
	l := unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "PodList"}}
	l.SetResourceVersion("1")
	l.Items = make([]unstructured.Unstructured, 0, syncPods)
	for i := range syncPods {
		var o unstructured.Unstructured
		o.SetAPIVersion("v1")
		o.SetKind("Pod")
		o.SetNamespace("default")
		o.SetName(fmt.Sprintf("p-%d", i))
		o.SetResourceVersion("1")
		l.Items = append(l.Items, o)
	}

	return &l
}

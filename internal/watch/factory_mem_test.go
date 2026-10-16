// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

const cachePods = 2_000

func BenchmarkCachePods(b *testing.B) {
	dyn := fakeDyn(b, cachePods)
	var heap uint64
	for b.Loop() {
		heap += cacheHeap(b, dyn)
	}
	b.ReportMetric(float64(heap)/float64(b.N*cachePods), "heap-B/pod")
}

func cacheHeap(b *testing.B, dyn dynamic.Interface) uint64 {
	f := NewFactory(dynConn{dyn: dyn})
	f.Start(client.BlankNamespace)
	defer func() {
		fac := f.FactoryFor(client.BlankNamespace)
		f.Terminate()
		fac.Shutdown()
	}()

	before := heapInUse()
	oo, err := f.List(client.PodGVR, client.BlankNamespace, true, labels.Everything())
	require.NoError(b, err)
	require.Len(b, oo, cachePods)
	after := heapInUse()
	runtime.KeepAlive(f)

	return after - before
}

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return m.HeapAlloc
}

func fakeDyn(tb testing.TB, n int) dynamic.Interface {
	raw, err := os.ReadFile("testdata/po_managed.json")
	require.NoError(tb, err)
	var po unstructured.Unstructured
	require.NoError(tb, json.Unmarshal(raw, &po.Object))

	oo := make([]kruntime.Object, 0, n)
	for i := range n {
		o := po.DeepCopy()
		o.SetName(fmt.Sprintf("p-%d", i))
		oo = append(oo, o)
	}
	kinds := map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"}

	return fake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), kinds, oo...)
}

type dynConn struct {
	client.Connection
	dyn dynamic.Interface
}

func (c dynConn) DynDial() (dynamic.Interface, error) { return c.dyn, nil }

func (dynConn) CanI(string, *client.GVR, string, []string) (bool, error) { return true, nil }

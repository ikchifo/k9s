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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const cachePods = 2_000

func BenchmarkCachePods(b *testing.B) {
	conn := fakeConn(b, cachePods)
	var heap uint64
	for b.Loop() {
		heap += cacheHeap(b, conn)
	}
	b.ReportMetric(float64(heap)/float64(b.N*cachePods), "heap-B/pod")
}

func cacheHeap(b *testing.B, conn dynConn) uint64 {
	f := NewFactory(conn)
	f.Start(client.BlankNamespace)
	defer f.Terminate()

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

func fakeConn(tb testing.TB, n int) dynConn {
	raw, err := os.ReadFile("testdata/po_managed.json")
	require.NoError(tb, err)
	var po v1.Pod
	require.NoError(tb, json.Unmarshal(raw, &po))

	oo := make([]kruntime.Object, 0, n)
	for i := range n {
		o := po.DeepCopy()
		o.Name = fmt.Sprintf("p-%d", i)
		oo = append(oo, o)
	}
	dyn := fake.NewSimpleDynamicClient(kruntime.NewScheme())

	return dynConn{dyn: dyn, pods: kubefake.NewSimpleClientset(oo...)}
}

type dynConn struct {
	client.Connection
	dyn  dynamic.Interface
	pods kubernetes.Interface
}

func (c dynConn) DynDial() (dynamic.Interface, error)    { return c.dyn, nil }
func (c dynConn) PodDial() (kubernetes.Interface, error) { return c.pods, nil }

func (dynConn) CanI(string, *client.GVR, string, []string) (bool, error) { return true, nil }

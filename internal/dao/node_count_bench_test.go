// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"strconv"
	"testing"

	"github.com/derailed/k9s/internal/client"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// synthPods builds n pods spread across the given node count.
func synthPods(n, nodes int) []runtime.Object {
	oo := make([]runtime.Object, 0, n)
	for i := range n {
		oo = append(oo, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "tables-" + strconv.Itoa(i)},
			Spec:       v1.PodSpec{NodeName: "tables-node" + strconv.Itoa(i%nodes)},
		})
	}

	return oo
}

func synthNodeNames(n int) []string {
	ss := make([]string, 0, n)
	for i := range n {
		ss = append(ss, "tables-node"+strconv.Itoa(i))
	}

	return ss
}

// scanPodsByNode replicates the previous per-node pod scan.
func scanPodsByNode(oo []runtime.Object, nodeName string) int {
	count := 0
	for _, o := range oo {
		p, err := client.ToPod(o)
		if err != nil {
			return count
		}
		if p.Spec.NodeName == nodeName {
			count++
		}
	}

	return count
}

// BenchmarkTablesPodCountScan measures the previous per-node scan.
func BenchmarkTablesPodCountScan(b *testing.B) {
	for _, size := range []struct{ nodes, pods int }{
		{100, 2000},
		{1000, 20000},
	} {
		oo := synthPods(size.pods, size.nodes)
		names := synthNodeNames(size.nodes)
		b.Run(strconv.Itoa(size.nodes)+"x"+strconv.Itoa(size.pods), func(b *testing.B) {
			b.ResetTimer()
			for range b.N {
				for _, name := range names {
					_ = scanPodsByNode(oo, name)
				}
			}
		})
	}
}

// BenchmarkTablesPodCountMap measures the one-pass node counts.
func BenchmarkTablesPodCountMap(b *testing.B) {
	for _, size := range []struct{ nodes, pods int }{
		{100, 2000},
		{1000, 20000},
	} {
		oo := synthPods(size.pods, size.nodes)
		names := synthNodeNames(size.nodes)
		b.Run(strconv.Itoa(size.nodes)+"x"+strconv.Itoa(size.pods), func(b *testing.B) {
			b.ResetTimer()
			for range b.N {
				counts := countPodsByNode(oo)
				for _, name := range names {
					_ = counts[name]
				}
			}
		})
	}
}

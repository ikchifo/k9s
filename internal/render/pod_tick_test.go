// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package render_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model1"
	"github.com/derailed/k9s/internal/render"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func BenchmarkPodRenderTick(b *testing.B) {
	const count = 20_000

	base := load(b, "po_full")
	oo := make([]runtime.Object, count)
	for i := range oo {
		u := base.DeepCopy()
		u.SetName("p-" + strconv.Itoa(i))
		u.SetResourceVersion(strconv.Itoa(i + 1))
		u.SetUID(types.UID("uid-" + strconv.Itoa(i)))
		oo[i] = &render.PodWithMetrics{Raw: u, MX: makePodMX(u.GetName(), "10m", "10Mi")}
	}
	td := model1.NewTableData(client.PodGVR)
	po := render.NewPod()
	require.NoError(b, td.Render(context.Background(), po, oo))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = td.Render(context.Background(), po, oo)
	}
}

func TestPodRenderTick(t *testing.T) {
	po := render.NewPod()
	td := model1.NewTableData(client.PodGVR)
	pod := load(t, "po_full")
	pod.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-3 * time.Second)))
	pwm := &render.PodWithMetrics{Raw: pod, MX: makePodMX(pod.GetName(), "10m", "10Mi")}

	assertFresh := func(t *testing.T) model1.Fields {
		t.Helper()
		require.NoError(t, td.Render(context.Background(), po, []runtime.Object{pwm}))
		var want model1.Row
		require.NoError(t, render.NewPod().Render(pwm, "", &want))
		got, ok := td.FindRow(want.ID)
		require.True(t, ok)
		assert.Equal(t, want.Fields, got.Row.Fields)

		return got.Row.Fields
	}

	t.Run("unchanged", func(t *testing.T) {
		first := assertFresh(t)
		assert.Equal(t, first, assertFresh(t))
	})

	t.Run("resource version", func(t *testing.T) {
		pod = pod.DeepCopy()
		pod.SetResourceVersion(pod.GetResourceVersion() + "1")
		pod.SetLabels(map[string]string{"app": "changed"})
		pwm = &render.PodWithMetrics{Raw: pod, MX: pwm.MX}
		assert.Contains(t, assertFresh(t), "app=changed")
	})

	t.Run("metrics", func(t *testing.T) {
		pwm = &render.PodWithMetrics{Raw: pod, MX: makePodMX(pod.GetName(), "250m", "300Mi")}
		ff := assertFresh(t)
		assert.Contains(t, ff, "250")
		assert.Contains(t, ff, "300")

		pwm = &render.PodWithMetrics{Raw: pod}
		assert.Equal(t, "0", assertFresh(t)[8])
	})

	t.Run("age", func(t *testing.T) {
		before := assertFresh(t)
		time.Sleep(time.Second)
		after := assertFresh(t)
		assert.NotEqual(t, before[len(before)-1], after[len(after)-1])
	})

	t.Run("fields not shared", func(t *testing.T) {
		assertFresh(t)[0] = "mutated"
		assert.NotEqual(t, "mutated", assertFresh(t)[0])
	})
}

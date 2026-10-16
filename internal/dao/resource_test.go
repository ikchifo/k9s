// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/watch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestResourceToYAMLManagedFields(t *testing.T) {
	raw, err := os.ReadFile("../watch/testdata/po_managed.json")
	require.NoError(t, err)
	var po unstructured.Unstructured
	require.NoError(t, json.Unmarshal(raw, &po.Object))
	require.NotEmpty(t, po.GetManagedFields())

	kinds := map[schema.GroupVersionResource]string{client.PodGVR.GVR(): "PodList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, &po)
	var pod v1.Pod
	require.NoError(t, json.Unmarshal(raw, &pod))
	pcs := kubefake.NewSimpleClientset(&pod)
	f := watch.NewFactory(dynConn{conn: makeConn(), dyn: dyn, pods: pcs})
	f.Start(client.BlankNamespace)
	t.Cleanup(f.Terminate)

	var r dao.Resource
	r.Init(f, client.PodGVR)
	path := client.FQN(po.GetNamespace(), po.GetName())

	cached, err := r.Get(t.Context(), path)
	require.NoError(t, err)
	assert.Empty(t, cached.(*v1.Pod).ManagedFields)

	hidden, err := r.ToYAML(path, false)
	require.NoError(t, err)
	assert.NotContains(t, hidden, "managedFields")

	shown, err := r.ToYAML(path, true)
	require.NoError(t, err)
	assert.Contains(t, shown, "managedFields")
	assert.Contains(t, shown, "manager: kube-controller-manager")
}

type dynConn struct {
	*conn
	dyn  dynamic.Interface
	pods kubernetes.Interface
}

func (c dynConn) DynDial() (dynamic.Interface, error)    { return c.dyn, nil }
func (c dynConn) PodDial() (kubernetes.Interface, error) { return c.pods, nil }

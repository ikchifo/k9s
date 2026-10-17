// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"context"
	"fmt"
	"sync"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
)

const (
	gvFmt       = "application/json;as=Table;v=%s;g=%s, application/json"
	includeMeta = "Metadata"
	includeObj  = "Object"
	includeNone = "None"
	header      = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json;as=Table;v=v1beta1;g=meta.k8s.io,application/json"

	colTypeStr = "string"
	nsColName  = "Namespace"
)

var genScheme = runtime.NewScheme()

// tableDial bundles a table REST client with its parameter codec.
type tableDial struct {
	client *rest.RESTClient
	params runtime.ParameterCodec
}

// tableDials caches dials per factory+gvr. A factory change (context switch)
// invalidates every entry. genGVs tracks registered group versions so scheme
// writes happen at most once per GV. All fields are guarded by mx.
var tableDials = struct {
	mx      sync.Mutex
	factory Factory
	entries map[client.GVR]*tableDial
	genGVs  map[schema.GroupVersion]bool
}{
	entries: make(map[client.GVR]*tableDial),
	genGVs:  make(map[schema.GroupVersion]bool),
}

// Table retrieves K8s resources as tabular data.
type Table struct {
	Generic
}

// Get returns a given resource.
func (t *Table) Get(ctx context.Context, path string) (runtime.Object, error) {
	d, err := t.dial()
	if err != nil {
		return nil, err
	}

	ns, n := client.Namespaced(path)
	a := fmt.Sprintf(gvFmt, metav1.SchemeGroupVersion.Version, metav1.GroupName)
	req := d.client.Get().
		SetHeader("Accept", a).
		Name(n).
		Resource(t.gvr.R()).
		VersionedParams(&metav1.TableOptions{}, d.params)
	if ns != client.ClusterScope {
		req = req.Namespace(ns)
	}

	return req.Do(ctx).Get()
}

// List all Resources in a given namespace.
func (t *Table) List(ctx context.Context, ns string) ([]runtime.Object, error) {
	sel := labels.Everything()
	if labelSel, ok := ctx.Value(internal.KeyLabels).(labels.Selector); ok {
		sel = labelSel
	}
	fieldSel, _ := ctx.Value(internal.KeyFields).(string)

	includeObject := includeMeta
	if t.includeObj {
		includeObject = includeObj
	}

	d, err := t.dial()
	if err != nil {
		return nil, err
	}
	o, err := d.client.Get().
		SetHeader("Accept", header).
		Param("includeObject", includeObject).
		Namespace(ns).
		Resource(t.gvr.R()).
		VersionedParams(&metav1.ListOptions{
			LabelSelector: sel.String(),
			FieldSelector: fieldSel,
		}, metav1.ParameterCodec).
		Do(ctx).Get()
	if err != nil {
		return nil, err
	}

	namespaced := true
	if res, e := MetaAccess.MetaFor(t.gvr); e == nil && !res.Namespaced {
		namespaced = false
	}
	ta, err := decodeTable(ctx, o.(*metav1.Table), namespaced)
	if err != nil {
		return nil, err
	}

	return []runtime.Object{ta}, nil
}

// ----------------------------------------------------------------------------
// Helpers...

func decodeTable(ctx context.Context, table *metav1.Table, namespaced bool) (runtime.Object, error) {
	if namespaced {
		table.ColumnDefinitions = append([]metav1.TableColumnDefinition{{Name: nsColName, Type: colTypeStr}}, table.ColumnDefinitions...)
	}
	pool := internal.NewWorkerPool(ctx, internal.DefaultPoolSize)
	for i := range table.Rows {
		pool.Add(func(_ context.Context) error {
			row := &table.Rows[i]
			if row.Object.Raw == nil || row.Object.Object != nil {
				return nil
			}
			converted, err := runtime.Decode(unstructured.UnstructuredJSONScheme, row.Object.Raw)
			if err != nil {
				return err
			}
			row.Object.Object = converted
			var m metav1.Object
			if obj := row.Object.Object; obj != nil {
				m, _ = meta.Accessor(obj)
			}
			var ns string
			if m != nil {
				ns = m.GetNamespace()
			}
			if namespaced {
				row.Cells = append([]any{ns}, row.Cells...)
			}
			return nil
		})
	}
	errs := pool.Drain()
	if len(errs) > 0 {
		return nil, fmt.Errorf("failed to decode table rows: %w", errs[0])
	}

	return table, nil
}

func (t *Table) dial() (*tableDial, error) {
	f := t.getFactory()

	tableDials.mx.Lock()
	defer tableDials.mx.Unlock()

	if tableDials.factory != f {
		tableDials.entries, tableDials.factory = make(map[client.GVR]*tableDial), f
	}
	if d, ok := tableDials.entries[*t.gvr]; ok {
		return d, nil
	}
	d, err := newTableDial(f, t.gvr)
	if err != nil {
		return nil, err
	}
	tableDials.entries[*t.gvr] = d

	return d, nil
}

func newTableDial(f Factory, gvr *client.GVR) (*tableDial, error) {
	registerTableTypes(gvr.GV())
	codecs := serializer.NewCodecFactory(genScheme)

	cfg, err := f.Client().RestConfig()
	if err != nil {
		return nil, err
	}
	gv := gvr.GV()
	cfg.GroupVersion = &gv
	cfg.APIPath = "/apis"
	if gvr.G() == "" {
		cfg.APIPath = "/api"
	}
	cfg.NegotiatedSerializer = codecs.WithoutConversion()
	rc, err := rest.RESTClientFor(cfg)
	if err != nil {
		return nil, err
	}

	return &tableDial{client: rc, params: runtime.NewParameterCodec(genScheme)}, nil
}

// registerTableTypes declares table types in the shared scheme once per group version.
func registerTableTypes(gv schema.GroupVersion) {
	if tableDials.genGVs[gv] {
		return
	}
	var (
		tt  metav1.Table
		opt metav1.TableOptions
	)
	metav1.AddToGroupVersion(genScheme, gv)
	genScheme.AddKnownTypes(gv, &tt, &opt)
	if !tableDials.genGVs[metav1.SchemeGroupVersion] {
		genScheme.AddKnownTypes(metav1.SchemeGroupVersion, &tt, &opt)
		tableDials.genGVs[metav1.SchemeGroupVersion] = true
	}
	tableDials.genGVs[gv] = true
}

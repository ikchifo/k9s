// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	restclient "k8s.io/client-go/rest"
	kjson "sigs.k8s.io/json"
)

var (
	metaV1     = schema.GroupVersion{Version: "v1"}
	metaScheme = newMetaScheme()
	eventGVK   = metaV1.WithKind(metav1.WatchEventKind)
)

func newMetaScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	metav1.AddToGroupVersion(s, metaV1)

	return s
}

// newDynamicClient builds a dynamic client whose JSON watch decoding parses each
// event once, instead of the stock serializer's sniff, validate and re-parse passes.
func newDynamicClient(cfg *restclient.Config) (*dynamic.DynamicClient, error) {
	dcfg := dynamic.ConfigFor(cfg)
	dcfg.NegotiatedSerializer = newWatchCodec(dcfg.NegotiatedSerializer)
	dcfg.GroupVersion = nil
	dcfg.APIPath = "/if-you-see-this-search-for-the-break"

	hc, err := restclient.HTTPClientFor(dcfg)
	if err != nil {
		return nil, err
	}
	rc, err := restclient.UnversionedRESTClientForConfigAndClient(dcfg, hc)
	if err != nil {
		return nil, err
	}

	return dynamic.New(rc), nil
}

type watchCodec struct {
	runtime.NegotiatedSerializer
	types []runtime.SerializerInfo
}

func newWatchCodec(ns runtime.NegotiatedSerializer) watchCodec {
	tt := slices.Clone(ns.SupportedMediaTypes())
	for i := range tt {
		if tt[i].MediaType != runtime.ContentTypeJSON || tt[i].StreamSerializer == nil {
			continue
		}
		ss := *tt[i].StreamSerializer
		ss.Serializer = eventDecoder{ss.Serializer}
		tt[i].StreamSerializer = &ss
		tt[i].Serializer = objectDecoder{tt[i].Serializer}
	}

	return watchCodec{NegotiatedSerializer: ns, types: tt}
}

func (c watchCodec) SupportedMediaTypes() []runtime.SerializerInfo {
	return c.types
}

type eventDecoder struct {
	runtime.Serializer
}

func (d eventDecoder) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	ev, ok := into.(*metav1.WatchEvent)
	if !ok || gvk != nil {
		return d.Serializer.Decode(data, gvk, into)
	}
	if err := kjson.UnmarshalCaseSensitivePreserveInts(data, ev); err != nil {
		return nil, nil, err
	}

	return ev, &eventGVK, nil
}

type objectDecoder struct {
	runtime.Serializer
}

// Decode handles untyped decodes (watch event objects); anything the stock
// serializer would type or reject goes back to it.
func (d objectDecoder) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	if into != nil || gvk != nil {
		return d.Serializer.Decode(data, gvk, into)
	}

	var m map[string]any
	if err := kjson.UnmarshalCaseSensitivePreserveInts(data, &m); err != nil {
		return d.Serializer.Decode(data, gvk, into)
	}
	u := &unstructured.Unstructured{Object: m}
	k := u.GroupVersionKind()
	if k.Kind == "" || k.Version == "" {
		return d.Serializer.Decode(data, gvk, into)
	}
	if _, err := metaScheme.New(k); err == nil {
		return d.Serializer.Decode(data, gvk, into)
	}

	return u, &k, nil
}

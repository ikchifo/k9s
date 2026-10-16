package main

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kjson "sigs.k8s.io/json"
)

var (
	metaScheme = newMetaScheme()
	eventGVK   = metav1.SchemeGroupVersion.WithKind(metav1.WatchEventKind)
)

func newMetaScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	metav1.AddToGroupVersion(s, schema.GroupVersion{Version: "v1"})
	return s
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

func (c watchCodec) SupportedMediaTypes() []runtime.SerializerInfo { return c.types }

type eventDecoder struct{ runtime.Serializer }

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

type objectDecoder struct{ runtime.Serializer }

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
	if k.Kind == "" || u.GetAPIVersion() == "" || metaScheme.Recognizes(k) {
		return d.Serializer.Decode(data, gvk, into)
	}
	return u, &k, nil
}

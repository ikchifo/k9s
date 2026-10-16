package main

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kjson "sigs.k8s.io/json"
)

var known = func() *runtime.Scheme {
	s := runtime.NewScheme()
	metav1.AddToGroupVersion(s, schema.GroupVersion{Version: "v1"})
	return s
}()

var eventGVK = metav1.SchemeGroupVersion.WithKind(metav1.WatchEventKind)

type fastEvents struct{ runtime.Serializer }

func (f fastEvents) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	ev, ok := into.(*metav1.WatchEvent)
	if !ok {
		return f.Serializer.Decode(data, gvk, into)
	}
	if err := kjson.UnmarshalCaseSensitivePreserveInts(data, ev); err != nil {
		return nil, nil, err
	}
	return ev, &eventGVK, nil
}

type fastObjects struct{ runtime.Serializer }

func (f fastObjects) Decode(data []byte, gvk *schema.GroupVersionKind, into runtime.Object) (runtime.Object, *schema.GroupVersionKind, error) {
	if into != nil || gvk != nil {
		return f.Serializer.Decode(data, gvk, into)
	}
	var m map[string]any
	if err := kjson.UnmarshalCaseSensitivePreserveInts(data, &m); err != nil {
		return f.Serializer.Decode(data, gvk, into)
	}
	u := &unstructured.Unstructured{Object: m}
	k := u.GroupVersionKind()
	if k.Kind == "" || u.GetAPIVersion() == "" || known.Recognizes(k) {
		return f.Serializer.Decode(data, gvk, into)
	}
	if _, err := known.New(k); err == nil {
		return f.Serializer.Decode(data, gvk, into)
	}
	return u, &k, nil
}

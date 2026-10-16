// Startup decode options for the k9s-l pods initial sync (20,170 pods).
// Inputs from perf/profiles/api (see api.md). Run with BENCH_DIR unset.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/streaming"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	restwatch "k8s.io/client-go/rest/watch"
)

const dir = "/private/var/folders/bs/z4bl587j47j1x1r2vjhd1srh0000gn/T/opencode/api"

var (
	podListPB = load(dir + "/pods-l.pb")
	events    = loadEvents(dir + "/watchstream-sample.json")
	typed     = scheme.Codecs.UniversalDeserializer()
)

func load(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	return b
}

func loadEvents(p string) []json.RawMessage {
	raw := load(p)
	var out []json.RawMessage
	depth, start := 0, -1
	for i, c := range raw {
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				var we struct{ Object json.RawMessage }
				if err := json.Unmarshal(raw[start:i+1], &we); err != nil {
					panic(err)
				}
				out = append(out, we.Object)
			}
		}
	}
	return out
}

// Today: each watch event object JSON -> unstructured.
func BenchmarkJSONToUnstructured(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		for _, o := range events {
			if _, _, err := unstructured.UnstructuredJSONScheme.Decode(o, nil, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func decodePB(b *testing.B) *v1.PodList {
	pl := &v1.PodList{}
	if _, _, err := typed.Decode(podListPB, nil, pl); err != nil {
		b.Fatal(err)
	}
	return pl
}

// Option b1: protobuf -> typed Pod only (a typed informer).
func BenchmarkProtobufTyped(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		decodePB(b)
	}
}

// Option b2: protobuf -> typed Pod -> unstructured (keeps the dao/render contract).
func BenchmarkProtobufToUnstructured(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		pl := decodePB(b)
		for i := range pl.Items {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pl.Items[i])
			if err != nil {
				b.Fatal(err)
			}
			_ = &unstructured.Unstructured{Object: m}
		}
	}
}

var stream = load(dir + "/watchstream-sample.json")

// The exact client-go dynamic watch chain: framer -> streaming decoder -> WatchEvent -> unstructured.
func BenchmarkClientGoWatchChain(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		w := dynamicWatcher(bytes.NewReader(stream))
		n := 0
		for {
			_, _, err := w.Decode()
			if err != nil {
				break
			}
			n++
		}
		if n != len(events) {
			b.Fatalf("n=%d want %d", n, len(events))
		}
	}
}

func dynamicWatcher(r io.Reader) *restwatch.Decoder { return watcher(r, false) }

func watcher(r io.Reader, fast bool) *restwatch.Decoder {
	ns := dynamic.ConfigFor(&rest.Config{}).NegotiatedSerializer
	info, _ := runtime.SerializerInfoForMediaType(ns.SupportedMediaTypes(), runtime.ContentTypeJSON)
	frames := info.StreamSerializer.Framer.NewFrameReader(io.NopCloser(r))
	evs, objs := info.StreamSerializer.Serializer, info.Serializer
	if fast {
		evs, objs = fastEvents{evs}, fastObjects{objs}
	}
	return restwatch.NewDecoder(streaming.NewDecoder(frames, evs), objs)
}

func BenchmarkFastWatchChain(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		w := watcher(bytes.NewReader(stream), true)
		n := 0
		for {
			if _, _, err := w.Decode(); err != nil {
				break
			}
			n++
		}
		if n != len(events) {
			b.Fatalf("n=%d", n)
		}
	}
}

func TestFastChainIdentical(t *testing.T) {
	a, f := watcher(bytes.NewReader(stream), false), watcher(bytes.NewReader(stream), true)
	n := 0
	for {
		t1, o1, e1 := a.Decode()
		t2, o2, e2 := f.Decode()
		if (e1 == nil) != (e2 == nil) {
			t.Fatalf("err mismatch %v %v", e1, e2)
		}
		if e1 != nil {
			break
		}
		if t1 != t2 || !reflect.DeepEqual(o1, o2) || reflect.TypeOf(o1) != reflect.TypeOf(o2) {
			t.Fatalf("event %d differs", n)
		}
		n++
	}
	t.Logf("%d events identical", n)
}

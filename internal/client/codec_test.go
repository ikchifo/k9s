// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/streaming"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	restclient "k8s.io/client-go/rest"
	restwatch "k8s.io/client-go/rest/watch"
)

const watchStream = `
{"type":"ADDED","object":{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p1","resourceVersion":"1"},"spec":{"n":1,"f":1.5,"big":9007199254740993}}}
{"type":"MODIFIED","object":{"apiVersion":"fred.io/v1","kind":"Fred","metadata":{"name":"f1"},"spec":{"a":[1,"b",null,{"c":true}]}}}
{"type":"ERROR","object":{"apiVersion":"v1","kind":"Status","status":"Failure","message":"too old","reason":"Expired","code":410}}
{"type":"BOOKMARK","object":{"apiVersion":"v1","kind":"Pod","metadata":{"resourceVersion":"9","annotations":{"k8s.io/initial-events-end":"true"}}}}
{"type":"DELETED","object":{"apiVersion":"v1","metadata":{"name":"nokind"}}}
{"type":"ADDED","object":{"apiVersion":"v1","kind":"PodList","metadata":{},"items":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p2"}}]}}
{"type":"ADDED","object":{"apiVersion":"v1","kind":"Pod","metadata":{"name":"bad"},"spec":[}}
`

type decoded struct {
	t   watch.EventType
	obj runtime.Object
	err error
}

func newWatchDecoder(r io.Reader, ns runtime.NegotiatedSerializer) *restwatch.Decoder {
	info, _ := runtime.SerializerInfoForMediaType(ns.SupportedMediaTypes(), runtime.ContentTypeJSON)
	frames := info.StreamSerializer.Framer.NewFrameReader(io.NopCloser(r))

	return restwatch.NewDecoder(streaming.NewDecoder(frames, info.StreamSerializer.Serializer), info.Serializer)
}

func decodeAll(r io.Reader, ns runtime.NegotiatedSerializer) []decoded {
	d := newWatchDecoder(r, ns)
	out := make([]decoded, strings.Count(strings.TrimSpace(watchStream), "\n")+1)
	for i := range out {
		t, o, err := d.Decode()
		out[i] = decoded{t: t, obj: o, err: err}
	}

	return out
}

func stockSerializer() runtime.NegotiatedSerializer {
	return dynamic.ConfigFor(&restclient.Config{}).NegotiatedSerializer
}

func TestWatchCodecMatchesStock(t *testing.T) {
	want := decodeAll(bytes.NewReader([]byte(watchStream)), stockSerializer())
	got := decodeAll(bytes.NewReader([]byte(watchStream)), newWatchCodec(stockSerializer()))

	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, want[i].t, got[i].t, "event %d", i)
		assert.IsType(t, want[i].obj, got[i].obj, "event %d", i)
		assert.Equal(t, want[i].obj, got[i].obj, "event %d", i)
		assert.Equal(t, want[i].err != nil, got[i].err != nil, "event %d: %v vs %v", i, want[i].err, got[i].err)
	}
}

func BenchmarkWatchDecode(b *testing.B) {
	stream := podStream(b, 2_000)
	for _, n := range []struct {
		name string
		ns   runtime.NegotiatedSerializer
	}{{"stock", stockSerializer()}, {"codec", newWatchCodec(stockSerializer())}} {
		b.Run(n.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(stream)))
			for b.Loop() {
				d := newWatchDecoder(bytes.NewReader(stream), n.ns)
				for {
					if _, _, err := d.Decode(); err != nil {
						break
					}
				}
			}
		})
	}
}

func podStream(b *testing.B, count int) []byte {
	raw, err := os.ReadFile("../render/testdata/po_full.json")
	require.NoError(b, err)
	var buf bytes.Buffer
	require.NoError(b, json.Compact(&buf, raw))
	pod := buf.String()

	var s bytes.Buffer
	for range count {
		fmt.Fprintf(&s, `{"type":"ADDED","object":%s}`+"\n", pod)
	}

	return s.Bytes()
}

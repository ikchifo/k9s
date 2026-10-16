// Decode benchmark for the k9s-l pods LIST (20,170 pods) in the encodings
// k9s could use. Inputs fetched via kubectl proxy on 18001 (see report).
// Run: BENCH_DIR=... env -u GOROOT go test -bench . -benchmem -run xxx
package main

import (
	"encoding/json"
	"os"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
)

var (
	dir          = "/private/var/folders/bs/z4bl587j47j1x1r2vjhd1srh0000gn/T/opencode/api"
	podList      = load(dir + "/pods-l-compact.json")   // 106.8MB compact JSON LIST
	podListPB    = load(dir + "/pods-l.pb")             // 67.7MB protobuf LIST
	tableObj     = load(dir + "/pods-l-table-obj.json") // 247.5MB Table includeObject=Object
	tableMeta    = load(dir + "/pods-l-table-meta.json")
	watchEvents  = loadEvents(dir + "/watchstream-sample.json") // WatchList initial ADDED events
	watchBytes   int64
	unstructSchm = unstructured.UnstructuredJSONScheme
	typedDecoder = scheme.Codecs.UniversalDeserializer()
)

func load(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	return b
}

// loadEvents splits the concatenated watch event stream into per-event byte
// slices — the framing the dynamic informer's watch decoder consumes.
func loadEvents(p string) [][]byte {
	raw := load(p)
	var out [][]byte
	depth, start := 0, -1
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				out = append(out, raw[start:i+1])
				watchBytes += int64(i + 1 - start)
			}
		}
	}
	return out
}

// Bench 1: dynamic-informer style whole-LIST decode (JSON -> unstructured).
func BenchmarkUnstructuredListJSON(b *testing.B) {
	b.SetBytes(int64(len(podList)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ll := &unstructured.UnstructuredList{}
		if _, _, err := unstructSchm.Decode(podList, nil, ll); err != nil {
			b.Fatal(err)
		}
		if len(ll.Items) != 20170 {
			b.Fatalf("items=%d", len(ll.Items))
		}
	}
}

// Bench 2: per watch-event decode (k9s WatchList path: 20,170 ADDED events).
// Mirrors the dynamic client's watch decoder: unmarshal the event envelope,
// then decode the embedded object as unstructured.
func BenchmarkWatchEventsUnstructured(b *testing.B) {
	b.SetBytes(watchBytes)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, ev := range watchEvents {
			var we struct {
				Object json.RawMessage
			}
			if err := json.Unmarshal(ev, &we); err != nil {
				b.Fatal(err)
			}
			if _, _, err := unstructSchm.Decode(we.Object, nil, nil); err != nil {
				b.Fatal(err)
			}
			n++
		}
		if n != len(watchEvents) {
			b.Fatal("count")
		}
	}
}

// Bench 3: typed client LIST decode (protobuf -> v1.PodList).
func BenchmarkTypedProtobuf(b *testing.B) {
	b.SetBytes(int64(len(podListPB)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pl := &v1.PodList{}
		if _, _, err := typedDecoder.Decode(podListPB, nil, pl); err != nil {
			b.Fatal(err)
		}
		if len(pl.Items) != 20170 {
			b.Fatalf("items=%d", len(pl.Items))
		}
	}
}

// Bench 4: dao.Table path with includeObject=Object (Table JSON + unstructured rows).
func BenchmarkTableObject(b *testing.B) {
	b.SetBytes(int64(len(tableObj)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var t metav1.Table
		if err := json.Unmarshal(tableObj, &t); err != nil {
			b.Fatal(err)
		}
		for j := range t.Rows {
			row := &t.Rows[j]
			if row.Object.Raw == nil {
				continue
			}
			if _, _, err := unstructSchm.Decode(row.Object.Raw, nil, nil); err != nil {
				b.Fatal(err)
			}
		}
		if len(t.Rows) != 20170 {
			b.Fatalf("rows=%d", len(t.Rows))
		}
	}
}

// Bench 5: dao.Table path with includeObject=Metadata (dao decodeTable decodes
// each row's PartialObjectMetadata into unstructured, same as Object path).
func BenchmarkTableMetadata(b *testing.B) {
	b.SetBytes(int64(len(tableMeta)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var t metav1.Table
		if err := json.Unmarshal(tableMeta, &t); err != nil {
			b.Fatal(err)
		}
		for j := range t.Rows {
			row := &t.Rows[j]
			if row.Object.Raw == nil {
				continue
			}
			if _, _, err := unstructSchm.Decode(row.Object.Raw, nil, nil); err != nil {
				b.Fatal(err)
			}
		}
		if len(t.Rows) != 20170 {
			b.Fatalf("rows=%d", len(t.Rows))
		}
	}
}

// Bench 6: table decode reading cells only (no per-row object decode) — what a
// Table-based UI could get away with if it trusted server-side cells.
func BenchmarkTableCellsOnly(b *testing.B) {
	b.SetBytes(int64(len(tableMeta)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var t metav1.Table
		if err := json.Unmarshal(tableMeta, &t); err != nil {
			b.Fatal(err)
		}
		if len(t.Rows) != 20170 {
			b.Fatalf("rows=%d", len(t.Rows))
		}
	}
}

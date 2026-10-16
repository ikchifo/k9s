// Package gobench decodes the same watch stream as the Rust prototype, in Go,
// so both languages are measured on identical bytes and targets.
package gobench

import (
	"bufio"
	"bytes"
	"os"
	"testing"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/json"
)

const streamEnv = "WATCH_STREAM"

type event[T any] struct {
	Type   string `json:"type"`
	Object T      `json:"object"`
}

func BenchmarkGoDecode(b *testing.B) {
	data, err := os.ReadFile(os.Getenv(streamEnv))
	if err != nil {
		b.Fatal(err)
	}

	b.Run("value", func(b *testing.B) { bench[map[string]any](b, data) })
	b.Run("typed", func(b *testing.B) { bench[v1.Pod](b, data) })
}

func bench[T any](b *testing.B, data []byte) {
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var ev event[T]
			if err := json.UnmarshalCaseSensitivePreserveInts(sc.Bytes(), &ev); err != nil {
				b.Fatal(err)
			}
		}
	}
}

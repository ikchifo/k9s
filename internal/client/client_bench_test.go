// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func setupConnectivityBench(b *testing.B) (*APIClient, *atomic.Int32) {
	b.Helper()

	versionCalls := &atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		versionCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(version.Info{Major: "1", Minor: "31", GitVersion: "v1.31.0"})
	})
	srv := httptest.NewTLSServer(mux)
	b.Cleanup(srv.Close)

	b.Setenv("HOME", b.TempDir())

	dir := b.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	content := `apiVersion: v1
kind: Config
current-context: bench
contexts:
- context:
    cluster: bench
    user: bench
  name: bench
clusters:
- cluster:
    server: ` + srv.URL + `
    insecure-skip-tls-verify: true
  name: bench
users:
- name: bench
  user:
    token: bench
`
	if err := os.WriteFile(kubeconfig, []byte(content), 0600); err != nil {
		b.Fatal(err)
	}
	flags := genericclioptions.NewConfigFlags(false)
	flags.KubeConfig = &kubeconfig

	return &APIClient{
		config: NewConfig(flags),
		cache:  cache.NewLRUExpireCache(cacheSize),
		connOK: true,
		log:    slog.Default(),
	}, versionCalls
}

// BenchmarkCheckConnectivity measures one periodic connectivity check, the
// GET /version liveness probe k9s fires every 15s.
func BenchmarkCheckConnectivity(b *testing.B) {
	a, versionCalls := setupConnectivityBench(b)

	b.ResetTimer()
	for range b.N {
		if !a.CheckConnectivity() {
			b.Fatal("connectivity check failed")
		}
	}
	b.ReportMetric(float64(versionCalls.Load())/float64(b.N), "reqs/op")
}

// Startup probe: time the pods initial sync at l outside the k9s UI.
// Modes: raw (read body only), informer (dynamic informer, as k9s), typed (typed protobuf informer).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"runtime/pprof"
	"runtime/trace"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var podGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func main() {
	mode := flag.String("mode", "informer", "raw|informer|typed")
	accept := flag.String("accept", "application/json", "raw mode Accept header")
	ahead := flag.Bool("ahead", false, "read response bodies ahead in a goroutine")
	h1 := flag.Bool("h1", false, "force HTTP/1.1")
	flag.Parse()

	cfg, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	must(err)
	cfg.QPS, cfg.Burst = 50, 50
	if *h1 {
		cfg.NextProtos = []string{"http/1.1"}
	}

	if p := os.Getenv("CPUPROF"); p != "" {
		f, _ := os.Create(p)
		must(pprof.StartCPUProfile(f))
		defer pprof.StopCPUProfile()
	}
	if p := os.Getenv("TRACE"); p != "" {
		f, _ := os.Create(p)
		must(trace.Start(f))
		defer trace.Stop()
	}
	t0 := time.Now()
	if mb, _ := strconv.Atoi(os.Getenv("BALLAST")); mb > 0 {
		ballast(mb)
		fmt.Printf("ballast %dMB in %.2fs\n", mb, time.Since(t0).Seconds())
	}
	switch *mode {
	case "raw":
		raw(cfg, *accept)
	case "decode", "pipe":
		decode(cfg, *mode == "pipe")
	case "informer", "fast":
		if *ahead {
			cfg.Wrap(readAhead)
		}
		dyn := dynamic.NewForConfigOrDie(cfg)
		if *mode == "fast" {
			dyn = fastDyn(cfg)
		}
		f := dynamicinformer.NewDynamicSharedInformerFactory(dyn, 10*time.Minute)
		sync(f.ForResource(podGVR).Informer())
	case "typed":
		cfg.ContentType = "application/vnd.kubernetes.protobuf"
		cs := kubernetes.NewForConfigOrDie(cfg)
		f := informers.NewSharedInformerFactory(cs, 10*time.Minute)
		sync(f.Core().V1().Pods().Informer())
	}

	if p := os.Getenv("MEMPROF"); p != "" {
		f, _ := os.Create(p)
		must(pprof.Lookup("allocs").WriteTo(f, 0))
		f.Close()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	gogc := debug.SetGCPercent(-1)
	fmt.Printf("mode=%s h1=%v wall=%.2fs heap=%dMB sys=%dMB gc=%d gogc=%d\n", *mode, *h1, time.Since(t0).Seconds(), ms.HeapAlloc>>20, ms.HeapSys>>20, ms.NumGC, gogc)
}

func sync(inf cache.SharedIndexInformer) {
	stop := make(chan struct{})
	go inf.Run(stop)
	cache.WaitForCacheSync(stop, inf.HasSynced)
	fmt.Println("items", len(inf.GetStore().ListKeys()))
}

func raw(cfg *rest.Config, accept string) {
	hc, err := rest.HTTPClientFor(cfg)
	must(err)
	url := cfg.Host + "/api/v1/pods?allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan&sendInitialEvents=true&timeoutSeconds=4&watch=true"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	must(err)
	req.Header.Set("Accept", accept)
	resp, err := hc.Do(req)
	must(err)
	defer resp.Body.Close()
	start := time.Now()
	buf := make([]byte, 1<<20)
	var n int64
	var lastAt time.Time
	for {
		k, err := resp.Body.Read(buf)
		if k > 0 {
			n += int64(k)
			lastAt = time.Now()
		}
		if err == io.EOF {
			break
		}
		must(err)
	}
	fmt.Printf("raw proto=%s bytes=%d lastData=%.2fs\n", resp.Proto, n, lastAt.Sub(start).Seconds())
}

func ptr[T any](v T) *T { return &v }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

type aheadRT struct{ next http.RoundTripper }

func readAhead(rt http.RoundTripper) http.RoundTripper { return aheadRT{rt} }

func (a aheadRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := a.next.RoundTrip(r)
	if err != nil || r.URL.Query().Get("watch") != "true" {
		return resp, err
	}
	pr, pw := io.Pipe()
	ch := make(chan []byte, 4096)
	go func() {
		defer close(ch)
		for {
			b := make([]byte, 64<<10)
			n, err := resp.Body.Read(b)
			if err != nil {
			}
			if n > 0 {
				ch <- b[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for b := range ch {
			if _, err := pw.Write(b); err != nil {
				break
			}
		}
		pw.Close()
	}()
	body := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{pr, closer(func() error { pr.Close(); return body.Close() })}
	return resp, nil
}

type closer func() error

func (c closer) Close() error { return c() }

// decode reads the WatchList stream and decodes each object to unstructured,
// inline (as client-go's StreamWatcher does) or in a second goroutine.
var kept []any

func decode(cfg *rest.Config, pipe bool) {
	hc, err := rest.HTTPClientFor(cfg)
	must(err)
	url := cfg.Host + "/api/v1/pods?allowWatchBookmarks=true&resourceVersionMatch=NotOlderThan&sendInitialEvents=true&timeoutSeconds=30&watch=true"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	must(err)
	resp, err := hc.Do(req)
	must(err)
	defer resp.Body.Close()

	ch := make(chan []byte, 1<<16)
	done := make(chan int)
	go func() {
		n := 0
		for o := range ch {
			if _, _, err := unstructured.UnstructuredJSONScheme.Decode(o, nil, nil); err != nil {
				panic(err)
			}
			n++
		}
		done <- n
	}()

	start := time.Now()
	dec := json.NewDecoder(resp.Body)
	var readAt time.Time
	for {
		var ev struct {
			Type   string
			Object json.RawMessage
		}
		must(dec.Decode(&ev))
		if ev.Type == "BOOKMARK" {
			readAt = time.Now()
			break
		}
		if pipe {
			ch <- ev.Object
			continue
		}
		o, _, err := unstructured.UnstructuredJSONScheme.Decode(ev.Object, nil, nil)
		must(err)
		if os.Getenv("KEEP") != "" {
			kept = append(kept, o)
		}
	}
	close(ch)
	n := <-done
	fmt.Printf("decode pipe=%v streamRead=%.2fs allDecoded=%.2fs piped=%d\n", pipe, readAt.Sub(start).Seconds(), time.Since(start).Seconds(), n)
}

var sink [][]byte

// ballast grows the heap with one large span, then frees it so later small spans reuse committed pages.
func ballast(mb int) {
	b := make([]byte, mb<<20)
	sink = append(sink, b)
	sink = nil
	runtime.GC()
}

func fastDyn(in *rest.Config) *dynamic.DynamicClient {
	cfg := dynamic.ConfigFor(in)
	cfg.NegotiatedSerializer = newWatchCodec(cfg.NegotiatedSerializer)
	cfg.GroupVersion = nil
	cfg.APIPath = "/if-you-see-this-search-for-the-break"
	hc, err := rest.HTTPClientFor(cfg)
	must(err)
	rc, err := rest.UnversionedRESTClientForConfigAndClient(cfg, hc)
	must(err)
	return dynamic.New(rc)
}

// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package client

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apimachinery/pkg/runtime/serializer/streaming"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	restwatch "k8s.io/client-go/rest/watch"
)

const (
	benchNamespace = "default"
	benchRV        = "1000"
	benchSyncWait  = 30 * time.Second
)

var benchGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

var (
	benchProto    = protobuf.NewSerializer(clientgoscheme.Scheme, clientgoscheme.Scheme)
	benchProtoRaw = protobuf.NewRawSerializer(clientgoscheme.Scheme, clientgoscheme.Scheme)
)

// benchPod builds a mid-size pod representative of a cluster workload:
// init container, two containers, volumes, probes and full status.
func benchPod(i int) *v1.Pod {
	name := fmt.Sprintf("bench-pod-%06d", i)
	podIP := fmt.Sprintf("10.244.%d.%d", i/250%250+1, i%250+1)
	ts := metav1.NewTime(time.Unix(1760000000, 0))
	node := fmt.Sprintf("bench-node-%02d", i%50)
	boolPtr := func(b bool) *bool { return &b }
	requests := v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse("100m"),
		v1.ResourceMemory: resource.MustParse("128Mi"),
	}
	limits := v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse("500m"),
		v1.ResourceMemory: resource.MustParse("512Mi"),
	}
	probe := func(path string) *v1.Probe {
		return &v1.Probe{
			ProbeHandler:  v1.ProbeHandler{HTTPGet: &v1.HTTPGetAction{Path: path, Port: intstr.FromInt32(8080)}},
			PeriodSeconds: 10,
		}
	}
	app := v1.Container{
		Name:  "app",
		Image: "registry.local/oss/app:1.42.0",
		Ports: []v1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: v1.ProtocolTCP}},
		Env: []v1.EnvVar{
			{Name: "HOST_NAME", ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
			{Name: "POD_IP", ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "status.podIP"}}},
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "REGION", Value: "eu-west-1"},
		},
		Resources:       v1.ResourceRequirements{Requests: requests, Limits: limits},
		ReadinessProbe:  probe("/ready"),
		LivenessProbe:   probe("/healthz"),
		VolumeMounts:    []v1.VolumeMount{{Name: "config", MountPath: "/etc/app", ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}},
		ImagePullPolicy: v1.PullIfNotPresent,
	}
	proxy := v1.Container{
		Name:  "proxy",
		Image: "docker.io/istio/proxyv2:1.22.0",
		Args:  []string{"proxy", "sidecar", "--domain", benchNamespace},
		Ports: []v1.ContainerPort{{Name: "metrics", ContainerPort: 15090, Protocol: v1.ProtocolTCP}},
		Resources: v1.ResourceRequirements{
			Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("10m"), v1.ResourceMemory: resource.MustParse("64Mi")},
			Limits:   v1.ResourceList{v1.ResourceCPU: resource.MustParse("250m"), v1.ResourceMemory: resource.MustParse("256Mi")},
		},
	}
	init := v1.Container{
		Name:    "init-config",
		Image:   "registry.local/oss/busybox:1.36",
		Command: []string{"sh", "-c", "cp /config/* /etc/app/"},
	}

	return &v1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       benchNamespace,
			UID:             types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", i)),
			ResourceVersion: benchRV,
			Labels: map[string]string{
				"app":               "bench",
				"pod-template-hash": fmt.Sprintf("h%05d", i%97),
				"tier":              "backend",
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/default-container": "app",
				"bench/instance": fmt.Sprintf("%d", i),
			},
			CreationTimestamp: ts,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "apps/v1",
				Kind:               "ReplicaSet",
				Name:               fmt.Sprintf("bench-h%05d", i%97),
				UID:                types.UID(fmt.Sprintf("00000000-0000-0000-0001-%012d", i%97)),
				Controller:         boolPtr(true),
				BlockOwnerDeletion: boolPtr(true),
			}},
		},
		Spec: v1.PodSpec{
			InitContainers: []v1.Container{init},
			Containers:     []v1.Container{app, proxy},
			Volumes: []v1.Volume{
				{Name: "config", VolumeSource: v1.VolumeSource{ConfigMap: &v1.ConfigMapVolumeSource{LocalObjectReference: v1.LocalObjectReference{Name: "bench-config"}}}},
				{Name: "tmp", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
			},
			NodeName:           node,
			ServiceAccountName: "bench",
			RestartPolicy:      v1.RestartPolicyAlways,
			DNSPolicy:          v1.DNSClusterFirst,
			SchedulerName:      "default-scheduler",
			Tolerations:        []v1.Toleration{{Key: "bench", Operator: v1.TolerationOpEqual, Value: "true", Effect: v1.TaintEffectNoSchedule}},
		},
		Status: v1.PodStatus{
			Phase:     v1.PodRunning,
			PodIP:     podIP,
			PodIPs:    []v1.PodIP{{IP: podIP}},
			HostIP:    fmt.Sprintf("192.168.%d.%d", i/250%250, i%250+1),
			QOSClass:  v1.PodQOSBurstable,
			StartTime: &ts,
			Conditions: []v1.PodCondition{
				{Type: v1.PodInitialized, Status: v1.ConditionTrue, LastTransitionTime: ts},
				{Type: v1.PodReady, Status: v1.ConditionTrue, LastTransitionTime: ts},
				{Type: v1.ContainersReady, Status: v1.ConditionTrue, LastTransitionTime: ts},
				{Type: v1.PodScheduled, Status: v1.ConditionTrue, LastTransitionTime: ts},
			},
			ContainerStatuses: []v1.ContainerStatus{
				{
					Name:         "app",
					State:        v1.ContainerState{Running: &v1.ContainerStateRunning{StartedAt: ts}},
					Ready:        true,
					RestartCount: 0,
					Image:        "registry.local/oss/app:1.42.0",
					ImageID:      "registry.local/oss/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
					ContainerID:  fmt.Sprintf("containerd://%012d", i),
				},
				{
					Name:         "proxy",
					State:        v1.ContainerState{Running: &v1.ContainerStateRunning{StartedAt: ts}},
					Ready:        true,
					RestartCount: 0,
					Image:        "docker.io/istio/proxyv2:1.22.0",
					ImageID:      "docker.io/istio/proxyv2@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					ContainerID:  fmt.Sprintf("containerd://%012d", i+1),
				},
			},
			InitContainerStatuses: []v1.ContainerStatus{{
				Name:         "init-config",
				State:        v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", StartedAt: ts, FinishedAt: ts}},
				Ready:        true,
				RestartCount: 0,
				Image:        "registry.local/oss/busybox:1.36",
				ImageID:      "registry.local/oss/busybox@sha256:2222222222222222222222222222222222222222222222222222222222222222",
				ContainerID:  fmt.Sprintf("containerd://%012d", i+2),
			}},
		},
	}
}

func benchPods(n int) []*v1.Pod {
	pods := make([]*v1.Pod, n)
	for i := range pods {
		pods[i] = benchPod(i)
	}

	return pods
}

func benchBookmark() *v1.Pod {
	return &v1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{ResourceVersion: benchRV, Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}},
	}
}

// benchStreams precomputes every wire format the fake apiserver serves, so
// benchmarks only measure the client-side decode and informer cost.
type benchStreams struct {
	jsonWatch, protoWatch []byte
	jsonList, protoList   []byte
	jsonPod, protoPod     int
}

func newBenchStreams(b *testing.B, pods []*v1.Pod) benchStreams {
	b.Helper()
	bookmark := benchBookmark()
	protoEncode := func(o runtime.Object) []byte {
		var buf bytes.Buffer
		require.NoError(b, benchProto.Encode(o, &buf))

		return buf.Bytes()
	}
	protoEvent := func(t watch.EventType, o runtime.Object) []byte {
		// Wire format: raw WatchEvent protobuf (no envelope), object bytes
		// carrying the full "k8s\x00"+Unknown envelope.
		var buf bytes.Buffer
		require.NoError(b, benchProtoRaw.Encode(&metav1.WatchEvent{Type: string(t), Object: runtime.RawExtension{Raw: protoEncode(o)}}, &buf))
		out := make([]byte, 4+buf.Len())
		binary.BigEndian.PutUint32(out, uint32(buf.Len()))
		copy(out[4:], buf.Bytes())

		return out
	}
	jsonEvent := func(t watch.EventType, o runtime.Object) []byte {
		raw, err := json.Marshal(o)
		require.NoError(b, err)
		line, err := json.Marshal(struct {
			Type   watch.EventType      `json:"type"`
			Object runtime.RawExtension `json:"object"`
		}{Type: t, Object: runtime.RawExtension{Raw: raw}})
		require.NoError(b, err)

		return append(line, '\n')
	}

	s := benchStreams{jsonPod: len(jsonEvent(watch.Added, pods[0]))}
	for _, p := range pods {
		s.jsonWatch = append(s.jsonWatch, jsonEvent(watch.Added, p)...)
		s.protoWatch = append(s.protoWatch, protoEvent(watch.Added, p)...)
	}
	s.jsonWatch = append(s.jsonWatch, jsonEvent(watch.Bookmark, bookmark)...)
	s.protoWatch = append(s.protoWatch, protoEvent(watch.Bookmark, bookmark)...)

	list := &v1.PodList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
		ListMeta: metav1.ListMeta{ResourceVersion: benchRV},
	}
	items := make([]v1.Pod, len(pods))
	for i, p := range pods {
		items[i] = *p
	}
	list.Items = items
	var err error
	s.jsonList, err = json.Marshal(list)
	require.NoError(b, err)
	s.protoList = protoEncode(list)
	s.protoPod = len(s.protoWatch) / (len(pods) + 1)

	return s
}

// benchServer mimics a WatchList-capable apiserver: watch requests stream
// length-framed events followed by the initial-events-end bookmark; list
// requests return the full collection.
type benchServer struct {
	*httptest.Server
	watchlistReqs atomic.Int64
}

func newBenchPodServer(b *testing.B, s *benchStreams) *benchServer {
	b.Helper()
	srv := &benchServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/"+benchNamespace+"/pods", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		proto := strings.Contains(r.Header.Get("Accept"), runtime.ContentTypeProtobuf)
		watching := q.Get("watch") == "true"

		var body []byte
		var ct string
		switch {
		case watching && proto:
			body, ct = s.protoWatch, runtime.ContentTypeProtobuf+";stream=watch"
		case watching:
			body, ct = s.jsonWatch, runtime.ContentTypeJSON+";stream=watch"
		case proto:
			body, ct = s.protoList, runtime.ContentTypeProtobuf
		default:
			body, ct = s.jsonList, runtime.ContentTypeJSON
		}
		if watching && q.Get("sendInitialEvents") == "true" {
			srv.watchlistReqs.Add(1)
		}

		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if watching {
			<-r.Context().Done()
		}
	})
	srv.Server = httptest.NewServer(mux)
	b.Cleanup(srv.Close)

	return srv
}

func waitSynced(b *testing.B, hasSynced func() bool) {
	b.Helper()
	deadline := time.Now().Add(benchSyncWait)
	for !hasSynced() {
		if time.Now().After(deadline) {
			b.Fatal("informer sync timed out")
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// BenchmarkPodInformerSync measures a full WatchList initial sync of the pods
// informer: protobuf typed informer (this branch) versus the dynamic JSON
// informer with the fast watch codec (previous behavior).
func BenchmarkPodInformerSync(b *testing.B) {
	for _, n := range []int{5000, 20000} {
		pods := benchPods(n)
		streams := newBenchStreams(b, pods)
		srv := newBenchPodServer(b, &streams)
		b.Logf("pods=%d wire bytes: json=%d proto=%d per pod", n, streams.jsonPod, streams.protoPod)

		b.Run(fmt.Sprintf("protobuf/%d", n), func(b *testing.B) {
			cfg := &restclient.Config{Host: srv.URL, QPS: 1000, Burst: 2000}
			cfg.ContentType = runtime.ContentTypeProtobuf
			cfg.AcceptContentTypes = runtime.ContentTypeProtobuf + "," + runtime.ContentTypeJSON
			cs, err := kubernetes.NewForConfig(cfg)
			require.NoError(b, err)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				stop := make(chan struct{})
				fact := informers.NewSharedInformerFactoryWithOptions(cs, 0, informers.WithNamespace(benchNamespace))
				inf := fact.Core().V1().Pods().Informer()
				fact.Start(stop)
				waitSynced(b, inf.HasSynced)
				close(stop)
			}
			b.StopTimer()
			require.GreaterOrEqual(b, int(srv.watchlistReqs.Load()), b.N)
			b.ReportMetric(float64(b.N*n)/b.Elapsed().Seconds(), "pods/s")
		})

		b.Run(fmt.Sprintf("json/%d", n), func(b *testing.B) {
			dyn, err := newDynamicClient(&restclient.Config{Host: srv.URL, QPS: 1000, Burst: 2000})
			require.NoError(b, err)

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				stop := make(chan struct{})
				fact := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dyn, 0, benchNamespace, nil)
				inf := fact.ForResource(benchGVR).Informer()
				fact.Start(stop)
				waitSynced(b, inf.HasSynced)
				close(stop)
			}
			b.StopTimer()
			require.GreaterOrEqual(b, int(srv.watchlistReqs.Load()), b.N)
			b.ReportMetric(float64(b.N*n)/b.Elapsed().Seconds(), "pods/s")
		})
	}
}

// BenchmarkWatchStreamDecode measures raw framed watch-stream decoding, no
// informer: the fast JSON codec into unstructured objects versus protobuf
// into typed pods.
func BenchmarkWatchStreamDecode(b *testing.B) {
	pods := benchPods(5000)
	streams := newBenchStreams(b, pods)
	want := len(pods) + 1

	b.Run("protobuf", func(b *testing.B) {
		benchDecode(b, streams.protoWatch, clientgoscheme.Codecs, runtime.ContentTypeProtobuf, want)
	})
	b.Run("fastjson", func(b *testing.B) {
		benchDecode(b, streams.jsonWatch, newWatchCodec(stockSerializer()), runtime.ContentTypeJSON, want)
	})
}

func benchDecode(b *testing.B, stream []byte, ns runtime.NegotiatedSerializer, mediaType string, want int) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for range b.N {
		d := benchWatchDecoder(bytes.NewReader(stream), ns, mediaType)
		events := 0
		for {
			_, obj, err := d.Decode()
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(b, err)
			switch obj.(type) {
			case *v1.Pod, *unstructured.Unstructured:
			default:
				b.Fatalf("unexpected object %T", obj)
			}
			events++
		}
		if events != want {
			b.Fatalf("decoded %d events, want %d", events, want)
		}
	}
	b.ReportMetric(float64(b.N*(want-1))/b.Elapsed().Seconds(), "pods/s")
}

func benchWatchDecoder(r io.Reader, ns runtime.NegotiatedSerializer, mediaType string) *restwatch.Decoder {
	info, ok := runtime.SerializerInfoForMediaType(ns.SupportedMediaTypes(), mediaType)
	if !ok {
		panic("unsupported media type: " + mediaType)
	}
	frames := info.StreamSerializer.Framer.NewFrameReader(io.NopCloser(r))

	return restwatch.NewDecoder(streaming.NewDecoder(frames, info.StreamSerializer.Serializer), info.Serializer)
}

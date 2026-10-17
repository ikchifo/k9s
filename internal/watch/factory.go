// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package watch

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/slogs"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	di "k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const (
	defaultResync   = 10 * time.Minute
	defaultWaitTime = 500 * time.Millisecond
	podAPIVersion   = "v1"
	podKind         = "Pod"
)

// Factory tracks various resource informers.
type Factory struct {
	factories    map[string]di.DynamicSharedInformerFactory
	podFactories map[string]informers.SharedInformerFactory
	// clusterInfs tracks informers started on the cluster-wide factory so
	// namespaced reads can reuse them without probing the factory.
	clusterInfs map[string]informers.GenericInformer
	client      client.Connection
	podClient   kubernetes.Interface
	stopChan    chan struct{}
	forwarders  Forwarders
	mx          sync.RWMutex
}

// NewFactory returns a new informers factory.
func NewFactory(clt client.Connection) *Factory {
	return &Factory{
		client:       clt,
		factories:    make(map[string]di.DynamicSharedInformerFactory),
		podFactories: make(map[string]informers.SharedInformerFactory),
		clusterInfs:  make(map[string]informers.GenericInformer),
		forwarders:   NewForwarders(),
	}
}

// Start initializes the informers until caller cancels the context.
func (f *Factory) Start(ns string) {
	f.mx.Lock()
	defer f.mx.Unlock()

	slog.Debug("Factory started", slogs.Namespace, ns)
	f.stopChan = make(chan struct{})
	for ns, fac := range f.factories {
		slog.Debug("Starting factory for ns", slogs.Namespace, ns)
		fac.Start(f.stopChan)
	}
	for ns, fac := range f.podFactories {
		slog.Debug("Starting pod factory for ns", slogs.Namespace, ns)
		fac.Start(f.stopChan)
	}
}

// Terminate terminates all watchers and forwards.
func (f *Factory) Terminate() {
	f.mx.Lock()
	defer f.mx.Unlock()

	if f.stopChan != nil {
		close(f.stopChan)
		f.stopChan = nil
	}
	for k := range f.factories {
		delete(f.factories, k)
	}
	for k := range f.podFactories {
		delete(f.podFactories, k)
	}
	for k := range f.clusterInfs {
		delete(f.clusterInfs, k)
	}
	f.podClient = nil
	f.forwarders.DeleteAll()
}

// List returns a resource collection.
func (f *Factory) List(gvr *client.GVR, ns string, wait bool, lbls labels.Selector) ([]runtime.Object, error) {
	if client.IsAllNamespace(ns) {
		ns = client.BlankNamespace
	}
	inf, err := f.CanForResource(ns, gvr, client.ListAccess)
	if err != nil {
		return nil, err
	}

	var oo []runtime.Object
	if client.IsClusterScoped(ns) {
		oo, err = inf.Lister().List(lbls)
	} else {
		oo, err = inf.Lister().ByNamespace(ns).List(lbls)
	}
	if !wait || (wait && inf.Informer().HasSynced()) {
		return oo, err
	}

	waitForSync(inf)
	if client.IsClusterScoped(ns) {
		return inf.Lister().List(lbls)
	}
	return inf.Lister().ByNamespace(ns).List(lbls)
}

// HasSynced checks if given informer is up to date.
func (f *Factory) HasSynced(gvr *client.GVR, ns string) (bool, error) {
	inf, err := f.CanForResource(ns, gvr, client.ListAccess)
	if err != nil {
		return false, err
	}

	return inf.Informer().HasSynced(), nil
}

// Get retrieves a given resource.
func (f *Factory) Get(gvr *client.GVR, fqn string, wait bool, _ labels.Selector) (runtime.Object, error) {
	ns, n := namespaced(fqn)
	if client.IsAllNamespace(ns) {
		ns = client.BlankNamespace
	}

	inf, err := f.CanForInstance(fqn, gvr, []string{client.GetVerb})
	if err != nil {
		return nil, err
	}
	var o runtime.Object
	if client.IsClusterScoped(ns) {
		o, err = inf.Lister().Get(n)
	} else {
		o, err = inf.Lister().ByNamespace(ns).Get(n)
	}
	if !wait || (wait && inf.Informer().HasSynced()) {
		return o, err
	}

	waitForSync(inf)
	if client.IsClusterScoped(ns) {
		return inf.Lister().Get(n)
	}

	return inf.Lister().ByNamespace(ns).Get(n)
}

// waitForSync hangs until the informer is synced or the wait time is up.
func waitForSync(inf informers.GenericInformer) {
	select {
	case <-inf.Informer().HasSyncedChecker().Done():
	case <-time.After(defaultWaitTime):
	}
}

// WaitForCacheSync waits for all factories to update their cache.
func (f *Factory) WaitForCacheSync() {
	for ns, fac := range f.factories {
		m := fac.WaitForCacheSync(f.stopChan)
		for k, v := range m {
			slog.Debug("CACHE `%q Loaded %t:%s",
				slogs.Namespace, ns,
				slogs.ResGrpVersion, v,
				slogs.ResKind, k,
			)
		}
	}
	for ns, fac := range f.podFactories {
		for k, v := range fac.WaitForCacheSync(f.stopChan) {
			slog.Debug("CACHE `%q Loaded %t:%s",
				slogs.Namespace, ns,
				slogs.ResGrpVersion, v,
				slogs.ResKind, k,
			)
		}
	}
}

// Client return the factory connection.
func (f *Factory) Client() client.Connection {
	return f.client
}

// FactoryFor returns a factory for a given namespace.
func (f *Factory) FactoryFor(ns string) di.DynamicSharedInformerFactory {
	return f.factories[ns]
}

// SetActiveNS sets the active namespace.
func (f *Factory) SetActiveNS(ns string) error {
	if f.isClusterWide() {
		return nil
	}
	_, err := f.ensureFactory(ns)
	return err
}

func (f *Factory) isClusterWide() bool {
	f.mx.RLock()
	defer f.mx.RUnlock()
	_, ok := f.factories[client.BlankNamespace]

	return ok
}

// CanForResource return an informer is user has access.
func (f *Factory) CanForResource(ns string, gvr *client.GVR, verbs []string) (informers.GenericInformer, error) {
	var resName string
	if gvr == client.NsGVR {
		resName = ns
	}
	auth, err := f.Client().CanI(ns, gvr, resName, verbs)
	if err != nil {
		return nil, err
	}
	if !auth {
		return nil, fmt.Errorf("%v access denied on resource %q:%q", verbs, ns, gvr)
	}

	// Namespaces are cluster-scoped; always use cluster scope for the informer
	if gvr == client.NsGVR {
		ns = client.ClusterScope
	}

	return f.ForResource(ns, gvr)
}

// CanForInstance return an informer is user has access.
func (f *Factory) CanForInstance(fqn string, gvr *client.GVR, verbs []string) (informers.GenericInformer, error) {
	ns, n := namespaced(fqn)
	if client.IsAllNamespace(ns) {
		ns = client.BlankNamespace
	}

	// For namespace resources, use the resource name as the namespace for RBAC
	// (RoleBindings within that namespace can grant permissions),
	// but keep the original ns for the informer since namespaces are cluster-scoped.
	authNs := ns
	if gvr == client.NsGVR {
		authNs = n
	}

	auth, err := f.Client().CanI(authNs, gvr, n, verbs)
	if err != nil {
		return nil, err
	}
	if !auth {
		return nil, fmt.Errorf("%v access denied on resource %q:%q", verbs, authNs, gvr)
	}

	return f.ForResource(ns, gvr)
}

// ForResource returns an informer for a given resource.
func (f *Factory) ForResource(ns string, gvr *client.GVR) (informers.GenericInformer, error) {
	if gvr == client.PodGVR {
		return f.podForResource(ns)
	}
	if inf, ok := f.syncedClusterInformer(ns, gvr); ok {
		return inf, nil
	}
	fact, err := f.ensureFactory(ns)
	if err != nil {
		return nil, err
	}
	inf := fact.ForResource(gvr.GVR())
	if inf == nil {
		slog.Error("No informer found",
			slogs.GVR, gvr,
			slogs.Namespace, ns,
		)
		return inf, nil
	}
	_ = inf.Informer().SetTransform(dropManagedFields)

	f.mx.Lock()
	fact.Start(f.stopChan)
	if client.IsClusterWide(ns) {
		f.clusterInfs[gvr.String()] = inf
	}
	f.mx.Unlock()

	return inf, nil
}

// syncedClusterInformer serves a namespaced read from a running cluster-wide
// informer when it is already synced, sparing a full LIST plus watch for
// every visited namespace.
func (f *Factory) syncedClusterInformer(ns string, gvr *client.GVR) (informers.GenericInformer, bool) {
	if client.IsClusterWide(ns) {
		return nil, false
	}

	f.mx.RLock()
	defer f.mx.RUnlock()
	inf, ok := f.clusterInfs[gvr.String()]
	if !ok || !inf.Informer().HasSynced() {
		return nil, false
	}

	return inf, true
}

// podForResource returns a typed informer serving cached *v1.Pod.
func (f *Factory) podForResource(ns string) (informers.GenericInformer, error) {
	fact, err := f.ensurePodFactory(ns)
	if err != nil {
		return nil, err
	}
	inf := fact.Core().V1().Pods().Informer()
	_ = inf.SetTransform(typedPodTransform)

	f.mx.RLock()
	defer f.mx.RUnlock()
	fact.Start(f.stopChan)

	return &typedInformer{informer: inf}, nil
}

func (f *Factory) ensurePodFactory(ns string) (informers.SharedInformerFactory, error) {
	if client.IsClusterWide(ns) {
		ns = client.BlankNamespace
	}
	f.mx.Lock()
	defer f.mx.Unlock()
	if fac, ok := f.podFactories[ns]; ok {
		return fac, nil
	}
	// The cluster-wide pod informer already lists every namespace: reuse it
	// and serve per-namespace reads through the lister instead of paying a
	// full LIST and an extra watch per namespace.
	if fac, ok := f.podFactories[client.BlankNamespace]; ok {
		return fac, nil
	}

	cs, err := f.podDial()
	if err != nil {
		return nil, err
	}
	f.podFactories[ns] = informers.NewSharedInformerFactoryWithOptions(
		cs,
		defaultResync,
		informers.WithNamespace(ns),
	)

	return f.podFactories[ns], nil
}

// podDialer lets connections override the typed pod clientset (used in tests).
type podDialer interface {
	PodDial() (kubernetes.Interface, error)
}

// podDial returns a typed clientset tuned for protobuf exchanges.
func (f *Factory) podDial() (kubernetes.Interface, error) {
	if f.podClient != nil {
		return f.podClient, nil
	}
	if pd, ok := f.client.(podDialer); ok {
		cs, err := pd.PodDial()
		if err != nil {
			return nil, err
		}
		f.podClient = cs

		return cs, nil
	}
	cfg, err := f.client.RestConfig()
	if err != nil {
		return nil, err
	}
	cfg.ContentType = runtime.ContentTypeProtobuf
	cfg.AcceptContentTypes = runtime.ContentTypeProtobuf + "," + runtime.ContentTypeJSON
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	f.podClient = cs

	return cs, nil
}

// typedInformer adapts a typed informer to a generic one.
type typedInformer struct {
	informer cache.SharedIndexInformer
}

var _ informers.GenericInformer = (*typedInformer)(nil)

func (t *typedInformer) Informer() cache.SharedIndexInformer {
	return t.informer
}

func (t *typedInformer) Lister() cache.GenericLister {
	return cache.NewGenericLister(t.informer.GetIndexer(), *client.PodGVR.GR())
}

// typedPodTransform trims pods for caching and restores type meta.
func typedPodTransform(o any) (any, error) {
	if p, ok := o.(*v1.Pod); ok {
		p.ManagedFields = nil
		p.TypeMeta = metav1.TypeMeta{APIVersion: podAPIVersion, Kind: podKind}
	}

	return o, nil
}

func (f *Factory) ensureFactory(ns string) (di.DynamicSharedInformerFactory, error) {
	if client.IsClusterWide(ns) {
		ns = client.BlankNamespace
	}
	f.mx.Lock()
	defer f.mx.Unlock()
	if fac, ok := f.factories[ns]; ok {
		return fac, nil
	}

	dial, err := f.client.DynDial()
	if err != nil {
		return nil, err
	}
	f.factories[ns] = di.NewFilteredDynamicSharedInformerFactory(
		dial,
		defaultResync,
		ns,
		nil,
	)

	return f.factories[ns], nil
}

func dropManagedFields(o any) (any, error) {
	if u, ok := o.(*unstructured.Unstructured); ok {
		u.SetManagedFields(nil)
	}

	return o, nil
}

// AddForwarder registers a new portforward for a given container.
func (f *Factory) AddForwarder(pf Forwarder) {
	f.mx.Lock()
	defer f.mx.Unlock()

	f.forwarders[pf.ID()] = pf
}

// DeleteForwarder deletes portforward for a given container.
func (f *Factory) DeleteForwarder(path string) {
	count := f.forwarders.Kill(path)
	slog.Warn("Deleted portforward",
		slogs.Count, count,
		slogs.GVR, path,
	)
}

// Forwarders returns all portforwards.
func (f *Factory) Forwarders() Forwarders {
	f.mx.RLock()
	defer f.mx.RUnlock()

	return f.forwarders
}

// ForwarderFor returns a portforward for a given container or nil if none exists.
func (f *Factory) ForwarderFor(path string) (Forwarder, bool) {
	f.mx.RLock()
	defer f.mx.RUnlock()

	fwd, ok := f.forwarders[path]

	return fwd, ok
}

// ValidatePortForwards check if pods are still around for portforwards.
// BOZO!! Review!!!
func (f *Factory) ValidatePortForwards() {
	for k, fwd := range f.forwarders {
		tokens := strings.Split(k, ":")
		if len(tokens) != 2 {
			slog.Error("Invalid port-forward key", slogs.Key, k)
			return
		}
		paths := strings.Split(tokens[0], "|")
		if len(paths) < 1 {
			slog.Error("Invalid port-forward path", slogs.Path, tokens[0])
		}
		o, err := f.Get(client.PodGVR, paths[0], false, labels.Everything())
		if err != nil {
			fwd.Stop()
			delete(f.forwarders, k)
			continue
		}
		pod, err := client.ToPod(o)
		if err != nil {
			continue
		}
		if pod.GetCreationTimestamp().Unix() > fwd.Age().Unix() {
			fwd.Stop()
			delete(f.forwarders, k)
		}
	}
}

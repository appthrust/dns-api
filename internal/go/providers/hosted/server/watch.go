package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hostedv1alpha1 "github.com/appthrust/dns-api/pkg/go/api/hosted/v1alpha1"
	"github.com/miekg/dns"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

var hostedZones = hostedv1alpha1.SchemeGroupVersion.WithResource("hostedzones")

type snapshotWatcher struct {
	client       dynamic.Interface
	index        *zoneIndex
	identity     string
	fieldManager string
	informer     cache.SharedIndexInformer
	registration cache.ResourceEventHandlerRegistration
	ready        atomic.Bool
	wake         chan struct{}
	connectionMu sync.Mutex
	connection   context.Context
	disconnect   context.CancelFunc
	acknowledged map[types.NamespacedName]acknowledgement // status worker; cleanup after it stops
}

type acknowledgement struct {
	uid  types.UID
	apex string
}

func newSnapshotWatcher(client dynamic.Interface, namespace, identity string, index *zoneIndex) (*snapshotWatcher, error) {
	w := &snapshotWatcher{client: client, identity: identity, fieldManager: "hosted-server/" + strings.SplitN(identity, "/", 2)[1], index: index, wake: make(chan struct{}, 1), acknowledged: make(map[types.NamespacedName]acknowledgement)}
	resource := client.Resource(hostedZones).Namespace(namespace)
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			return resource.List(ctx, options)
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			upstream, err := resource.Watch(ctx, options)
			if err != nil {
				return nil, err
			}
			connection, cancel := context.WithCancel(ctx)
			w.connectionMu.Lock()
			if w.disconnect != nil {
				w.disconnect()
			}
			w.connection, w.disconnect = connection, cancel
			w.connectionMu.Unlock()
			w.signal()
			tracked := &trackedWatch{upstream: upstream, result: make(chan watch.Event), cancel: cancel}
			go tracked.forward(connection)
			return tracked, nil
		},
	}
	w.informer = cache.NewSharedIndexInformer(cache.ToListWatcherWithWatchListSemantics(lw, client), &unstructured.Unstructured{}, 0, cache.Indexers{
		"apex": func(object any) ([]string, error) {
			value, ok := object.(*unstructured.Unstructured)
			if !ok || !hasZoneUnitOwner(value.GetOwnerReferences()) {
				return nil, nil
			}
			apex := snapshotApex(value)
			if apex == "" {
				return nil, nil
			}
			return []string{apex}, nil
		},
	})
	registration, err := w.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: w.load,
		UpdateFunc: func(old, next any) {
			oldObject, oldOK := old.(*unstructured.Unstructured)
			nextObject, nextOK := next.(*unstructured.Unstructured)
			if oldOK && nextOK && oldObject.GetUID() == nextObject.GetUID() && reflect.DeepEqual(oldObject.GetOwnerReferences(), nextObject.GetOwnerReferences()) {
				generation := oldObject.GetGeneration()
				if generation > 0 && generation == nextObject.GetGeneration() {
					return
				}
				// Fake clients and pre-generation objects need the structural
				// comparison; real status updates never scan the record payload.
				if generation == 0 && reflect.DeepEqual(oldObject.Object["spec"], nextObject.Object["spec"]) {
					return
				}
			}
			if oldOK && nextOK && hasZoneUnitOwner(oldObject.GetOwnerReferences()) && (snapshotApex(oldObject) != snapshotApex(nextObject) || !hasZoneUnitOwner(nextObject.GetOwnerReferences())) {
				w.reconcileApex(snapshotApex(oldObject))
			}
			w.load(next)
		},
		DeleteFunc: func(object any) {
			if tombstone, ok := object.(cache.DeletedFinalStateUnknown); ok {
				object = tombstone.Obj
			}
			if object, ok := object.(*unstructured.Unstructured); ok && hasZoneUnitOwner(object.GetOwnerReferences()) {
				w.index.remove(types.NamespacedName{Namespace: object.GetNamespace(), Name: object.GetName()}, object.GetUID())
				w.reconcileApex(snapshotApex(object))
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("register hosted snapshot handler: %w", err)
	}
	w.registration = registration
	return w, nil
}

func (w *snapshotWatcher) load(object any) {
	value, ok := object.(*unstructured.Unstructured)
	if !ok {
		return
	}
	if !hasZoneUnitOwner(value.GetOwnerReferences()) {
		w.index.metrics.rejected.WithLabelValues("unowned").Inc()
		w.index.remove(types.NamespacedName{Namespace: value.GetNamespace(), Name: value.GetName()}, value.GetUID())
		w.signal()
		return
	}
	w.reconcileApex(snapshotApex(value))
}

func snapshotApex(object *unstructured.Unstructured) string {
	domain, _, _ := unstructured.NestedString(object.Object, "spec", "domainName")
	if domain == "" {
		return ""
	}
	return dns.CanonicalName(domain)
}

// The informer's secondary index arbitrates all observed owners, not only the
// zones admitted under MaxZones. Conflicting snapshots stay in the informer,
// never in the compiled serving index. Deleting/renaming a contender reloads
// the sole survivor without keeping a second copy of every snapshot.
func (w *snapshotWatcher) reconcileApex(apex string) {
	if apex == "" {
		return
	}
	defer w.signal()
	objects, err := w.informer.GetIndexer().ByIndex("apex", apex)
	if err != nil {
		slog.Error("index hosted apex", "apex", apex, "error", err)
		return
	}
	if len(objects) != 1 {
		w.index.reconcileAbsentApex(apex, len(objects) > 1)
		return
	}
	value := objects[0].(*unstructured.Unstructured)
	var snapshot hostedv1alpha1.HostedZone
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(value.Object, &snapshot); err != nil {
		slog.Error("decode hosted snapshot", "namespace", value.GetNamespace(), "name", value.GetName(), "error", err)
		return
	}
	if err := w.index.replace(&snapshot); err != nil {
		slog.Error("load hosted snapshot", "namespace", snapshot.Namespace, "name", snapshot.Name, "error", err)
	}
}

func (w *snapshotWatcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *snapshotWatcher) liveContext() context.Context {
	w.connectionMu.Lock()
	defer w.connectionMu.Unlock()
	return w.connection
}

func (w *snapshotWatcher) run(ctx context.Context) {
	done := make(chan struct{})
	go func() { defer close(done); w.informer.RunWithContext(ctx) }()
	defer func() { <-done }()
	if !cache.WaitForCacheSync(ctx.Done(), w.registration.HasSynced) {
		return
	}
	w.ready.Store(true)
	defer w.ready.Store(false)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	// Unchanged snapshots heartbeat every 30 seconds while connected, within
	// the controller's fixed three-minute serving freshness window.
	reported := make(map[*zone]time.Time)
	for {
		w.reportDue(ctx, reported, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

func (w *snapshotWatcher) reportDue(ctx context.Context, reported map[*zone]time.Time, now time.Time) {
	for z := range reported {
		current, exists := w.index.zones.Load(z.apex)
		if !exists || current != z {
			delete(reported, z)
		}
	}
	connection := w.liveContext()
	if connection == nil || connection.Err() != nil || ctx.Err() != nil {
		return
	}
	// A quarantined or unowned snapshot is no longer being served. Relinquish
	// its old acknowledgement instead of waiting for the freshness window.
	for key, acknowledged := range w.acknowledged {
		if current, exists := w.index.zones.Load(acknowledged.apex); exists && current.(*zone).key == key && current.(*zone).uid == acknowledged.uid {
			continue
		}
		requestContext, cancel := context.WithTimeout(connection, 5*time.Second)
		err := w.applyStatus(requestContext, &zone{key: key, uid: acknowledged.uid, apex: acknowledged.apex}, nil)
		cancel()
		if err != nil && connection.Err() == nil {
			slog.Warn("remove unserved hosted acknowledgement", "zone", acknowledged.apex, "error", err)
		}
	}
	w.index.zones.Range(func(_, value any) bool {
		z := value.(*zone)
		const interval = 30 * time.Second
		if now.Sub(reported[z]) < interval {
			return true
		}
		if connection.Err() != nil || ctx.Err() != nil {
			return false
		}
		requestContext, cancel := context.WithTimeout(connection, 5*time.Second)
		err := w.applyStatus(requestContext, z, &hostedv1alpha1.HostedZoneServerStatus{Endpoint: w.identity, Serial: z.serial, ContentDigest: z.digest, ObservedAt: metav1.NewTime(now)})
		cancel()
		if err != nil {
			if connection.Err() == nil {
				slog.Error("acknowledge hosted snapshot", "zone", z.apex, "error", err)
			}
		} else {
			reported[z] = now
		}
		return true
	})
}

func (w *snapshotWatcher) applyStatus(ctx context.Context, z *zone, status *hostedv1alpha1.HostedZoneServerStatus) error {
	servers := []hostedv1alpha1.HostedZoneServerStatus{}
	if status != nil {
		servers = append(servers, *status)
	}
	// UID prevents an old in-flight acknowledgement or shutdown cleanup from
	// attaching itself to a deleted-and-recreated snapshot with the same name.
	patch := struct {
		APIVersion string            `json:"apiVersion"`
		Kind       string            `json:"kind"`
		Metadata   metav1.ObjectMeta `json:"metadata"`
		Status     struct {
			Servers []hostedv1alpha1.HostedZoneServerStatus `json:"servers"`
		} `json:"status"`
	}{APIVersion: hostedv1alpha1.SchemeGroupVersion.String(), Kind: "HostedZone", Metadata: metav1.ObjectMeta{Name: z.key.Name, Namespace: z.key.Namespace, UID: z.uid}}
	patch.Status.Servers = servers
	payload, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = w.client.Resource(hostedZones).Namespace(z.key.Namespace).Patch(ctx, z.key.Name, types.ApplyPatchType, payload, metav1.PatchOptions{FieldManager: w.fieldManager}, "status")
	if status == nil && apierrors.IsNotFound(err) {
		err = nil
	}
	if err == nil {
		if status == nil {
			delete(w.acknowledged, z.key)
		} else {
			w.acknowledged[z.key] = acknowledgement{uid: z.uid, apex: z.apex}
		}
	}
	return err
}

func (w *snapshotWatcher) removeOwnStatus(ctx context.Context) {
	for key, acknowledged := range w.acknowledged {
		z := &zone{key: key, uid: acknowledged.uid, apex: acknowledged.apex}
		if err := w.applyStatus(ctx, z, nil); err != nil {
			slog.Warn("remove hosted server acknowledgement", "zone", z.apex, "error", err)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// trackedWatch closes its connection context on EOF, ERROR, or Stop. This
// cancels in-flight status patches as well as suppressing new acknowledgements,
// without discarding the last successfully loaded serving index.
type trackedWatch struct {
	upstream watch.Interface
	result   chan watch.Event
	cancel   context.CancelFunc
	once     sync.Once
}

func (w *trackedWatch) Stop()                          { w.once.Do(func() { w.cancel(); w.upstream.Stop() }) }
func (w *trackedWatch) ResultChan() <-chan watch.Event { return w.result }
func (w *trackedWatch) forward(ctx context.Context) {
	defer close(w.result)
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.upstream.ResultChan():
			if !ok {
				return
			}
			if event.Type == watch.Error {
				w.cancel()
				// Delivery is best-effort: the reflector also reconnects on EOF.
				select {
				case w.result <- event:
				default:
				}
				return
			}
			select {
			case w.result <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}

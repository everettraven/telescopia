package cache

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ crcache.Informer = &ScopeInformer{}

// ScopeInformer is a wrapper around a client-go
// informer that is meant to store information
// needed for the dynamic scoped cache
type ScopeInformer struct {
	// The actual client-go informer itself
	informer informers.GenericInformer

	// The context.Context that will be
	// used to run the informer
	ctx context.Context

	// The context.CancelFunc to terminate
	// the informer
	cancel context.CancelFunc

	// The resources that are dependent
	// on this informer
	dependents map[types.UID]metav1.Object

	eventListeners map[string]cache.ResourceEventHandler
}

func NewScopeInformer(informer informers.GenericInformer) *ScopeInformer {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScopeInformer{
		informer:       informer,
		ctx:            ctx,
		cancel:         cancel,
		dependents:     make(map[types.UID]metav1.Object),
		eventListeners: make(map[string]cache.ResourceEventHandler),
	}
}

func (si *ScopeInformer) Run() {
	si.informer.Informer().Run(si.ctx.Done())
}

func (si *ScopeInformer) Terminate() {
	si.cancel()
}

func (si *ScopeInformer) GetDependents() map[types.UID]metav1.Object {
	return si.dependents
}

func (si *ScopeInformer) AddDependent(dependent metav1.Object) {
	if _, ok := si.dependents[dependent.GetUID()]; !ok {
		si.dependents[dependent.GetUID()] = dependent
	}
}

func (si *ScopeInformer) RemoveDependent(dependent metav1.Object) {
	delete(si.dependents, dependent.GetUID())
}

func (si *ScopeInformer) HasDependent(dependent metav1.Object) bool {
	_, ok := si.dependents[dependent.GetUID()]
	return ok
}

func (si *ScopeInformer) Get(key string) (runtime.Object, error) {
	return si.informer.Lister().Get(key)
}

func (si *ScopeInformer) List(listOpts client.ListOptions) ([]runtime.Object, error) {
	if listOpts.Namespace != corev1.NamespaceAll {
		return si.informer.Lister().ByNamespace(listOpts.Namespace).List(listOpts.LabelSelector)
	}

	return si.informer.Lister().List(listOpts.LabelSelector)
}

func (si *ScopeInformer) AddEventHandler(handler cache.ResourceEventHandler) {
	key := fmt.Sprintf("scopeinformer-addeventhandler-%s", hashObject(handler))
	// only add the event listener if it hasn't already been added to this informer
	if _, ok := si.eventListeners[key]; !ok {
		si.eventListeners[key] = handler
		si.informer.Informer().AddEventHandler(handler)
	}
}

func (si *ScopeInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) {
	key := fmt.Sprintf("scopeinformer-addeventhandlerwithresyncperiod-%s", hashObject(handler))
	// only add the event listener if it hasn't already been added to this informer
	if _, ok := si.eventListeners[key]; !ok {
		si.eventListeners[key] = handler
		si.informer.Informer().AddEventHandlerWithResyncPeriod(handler, resyncPeriod)
	}
}

func (si *ScopeInformer) AddIndexers(indexers cache.Indexers) error {
	return si.informer.Informer().AddIndexers(indexers)
}

func (si *ScopeInformer) HasSynced() bool {
	return si.informer.Informer().HasSynced()
}

func (si *ScopeInformer) SetWatchErrorHandler(handler cache.WatchErrorHandler) error {
	return si.informer.Informer().SetWatchErrorHandler(handler)
}

// Informers is a mapping of a string
// (meant to be a unique string) to a ScopeInformer
type Informers map[string]*ScopeInformer

// GvkToInformers is a mapping of GVK to Informers
type GvkToInformers map[schema.GroupVersionKind]Informers

// InformerOptions are meant to set options
// when adding an informer to a cache
type InformerOptions struct {
	// Namespace the informer is watching
	Namespace string
	// GVK the informer is watching
	Gvk schema.GroupVersionKind
	// Unique identifier for the informer
	Key string
	// The informer itself
	Informer informers.GenericInformer
	// The resource dependent on the informer
	Dependent metav1.Object
}

type InformerNotFoundErr struct {
	Err error
}

func NewInformerNotFoundErr(err error) *InformerNotFoundErr {
	return &InformerNotFoundErr{
		Err: err,
	}
}

func (infe *InformerNotFoundErr) Error() string {
	return infe.Err.Error()
}

func IsInformerNotFoundErr(err error) bool {
	_, ok := err.(*InformerNotFoundErr)
	return ok
}

func WatchErrorHandlerForScopeInformer(si *ScopeInformer, removeFromCache func()) func(r *cache.Reflector, err error) {
	return func(r *cache.Reflector, err error) {
		if errors.IsForbidden(err) {
			// reset dependents
			si.dependents = make(map[types.UID]metav1.Object)
			// remove from cache
			removeFromCache()
			// terminate informer
			si.Terminate()
		}
	}
}

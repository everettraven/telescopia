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

	// eventListeners is to keep track of
	// all ResourceEventHandlers to prevent
	// the same one from being added multiple times
	eventListeners map[string]cache.ResourceEventHandler
}

// NewScopeInformer will create an return a new ScopeInformer
// that wraps the provided informer.
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

// Run will run the ScopeInformer
func (si *ScopeInformer) Run() {
	si.informer.Informer().Run(si.ctx.Done())
}

// Terminate will kill the ScopeInformer
func (si *ScopeInformer) Terminate() {
	si.cancel()
}

// GetDependents will return the dependents
// for the ScopeInformer
func (si *ScopeInformer) GetDependents() map[types.UID]metav1.Object {
	return si.dependents
}

// AddDependent will add a dependent
// for the ScopeInformer
func (si *ScopeInformer) AddDependent(dependent metav1.Object) {
	if _, ok := si.dependents[dependent.GetUID()]; !ok {
		si.dependents[dependent.GetUID()] = dependent
	}
}

// RemoveDependent will remove a dependent
// for the ScopeInformer
func (si *ScopeInformer) RemoveDependent(dependent metav1.Object) {
	delete(si.dependents, dependent.GetUID())
}

// HasDependent checks if a dependent
// exists for a ScopeInformer
func (si *ScopeInformer) HasDependent(dependent metav1.Object) bool {
	_, ok := si.dependents[dependent.GetUID()]
	return ok
}

// Get will attempt to get a Kubernetes object
// with the given key
func (si *ScopeInformer) Get(key string) (runtime.Object, error) {
	return si.informer.Lister().Get(key)
}

// List will list kubernetes resources based
// on the provided list options
func (si *ScopeInformer) List(listOpts client.ListOptions) ([]runtime.Object, error) {
	if listOpts.Namespace != corev1.NamespaceAll {
		return si.informer.Lister().ByNamespace(listOpts.Namespace).List(listOpts.LabelSelector)
	}

	return si.informer.Lister().List(listOpts.LabelSelector)
}

// AddEventHandler will add an event handler
// to the ScopeInformer
func (si *ScopeInformer) AddEventHandler(handler cache.ResourceEventHandler) {
	key := fmt.Sprintf("scopeinformer-addeventhandler-%s", hashObject(handler))
	// only add the event listener if it hasn't already been added to this informer
	if _, ok := si.eventListeners[key]; !ok {
		si.eventListeners[key] = handler
		si.informer.Informer().AddEventHandler(handler)
	}
}

// AddEventHandlerWithResyncPeriod will add an event handler
// with a specific resync period to the ScopeInformer
func (si *ScopeInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) {
	key := fmt.Sprintf("scopeinformer-addeventhandlerwithresyncperiod-%s", hashObject(handler))
	// only add the event listener if it hasn't already been added to this informer
	if _, ok := si.eventListeners[key]; !ok {
		si.eventListeners[key] = handler
		si.informer.Informer().AddEventHandlerWithResyncPeriod(handler, resyncPeriod)
	}
}

// AddIndexers will add the provided indexers
// to the ScopeInformer
func (si *ScopeInformer) AddIndexers(indexers cache.Indexers) error {
	return si.informer.Informer().AddIndexers(indexers)
}

// HasSynced will return whether or not
// the ScopeInformer has synced
func (si *ScopeInformer) HasSynced() bool {
	return si.informer.Informer().HasSynced()
}

// SetWatchErrorHandler will set the watch
// error handler for the ScopeInformer
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

// InformerNotFoundErr is an error to
// represent that an informer was not
// found for the given request
type InformerNotFoundErr struct {
	Err error
}

// NewInformerNotFoundErr creates a new InformerNotFoundErr
// for the provided error
func NewInformerNotFoundErr(err error) *InformerNotFoundErr {
	return &InformerNotFoundErr{
		Err: err,
	}
}

// Error returns the string representation of the error
func (infe *InformerNotFoundErr) Error() string {
	return infe.Err.Error()
}

// IsInformerNotFoundErr checks whether or not the
// provided error is of type InformerNotFoundError
func IsInformerNotFoundErr(err error) bool {
	_, ok := err.(*InformerNotFoundErr)
	return ok
}

// WatchErrorHandlerForScopeInformer is a helper for
// creating a WatchErrorHandler for a given ScopeInformer
// and function that is called to remove the ScopeInformer
// from the cache.
func WatchErrorHandlerForScopeInformer(si *ScopeInformer, removeFromCache func()) func(r *cache.Reflector, err error) {
	return func(r *cache.Reflector, err error) {
		if errors.IsForbidden(err) {
			removeFromCache()
		}
	}
}

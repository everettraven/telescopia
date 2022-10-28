package cache

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TODO: Is this necessary for any reason?
type DynamicScopedCache interface {
	// Get will return a runtime.Object for a given key and GVK.
	// If an error is encountered, the error will be returned.
	// It will only attempt to use the existing informers to
	// find the requested resource and will NOT automatically
	// create a new informer if it cannot be found.
	Get(key types.NamespacedName, gvk schema.GroupVersionKind) (runtime.Object, error)

	// List will return a list of runtime.Objects for a given key and GVK.
	// If an error is encountered, the error will be returned.
	// It will only attempt to use the existing informers to
	// list and will NOT automatically create a new informer.
	List(listOpts client.ListOptions, gvk schema.GroupVersionKind) ([]runtime.Object, error)

	// AddInformer will take in InformerOptions as a parameter
	// and will use these options to add a new informer to the cache.
	// If the informer already exists, a new dependent will be added
	// to the existing informer.
	AddInformer(infOpts InformerOptions)

	// RemoveInformer will take in InformerOptions as a parameter
	// and will use these options to remove an existing informer from the cache.
	// an informer will only be fully removed from the cache if the
	// informer has no more dependents.
	RemoveInformer(infOpts InformerOptions)

	// Start will start the cache
	Start()

	// IsStarted returns a boolean that represents whether
	// or not the cache has been started
	IsStarted() bool
}

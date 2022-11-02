package cache

import (
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NamespaceScopedCache is a dynamic cache
// with a focus on only tracking informers
// with watches at a namespace level
type NamespaceScopedCache struct {
	Namespaces map[string]GvkToInformers
	started    bool
	mu         sync.Mutex
}

// NewNamespaceScopedCache will return a new
// NamespaceScopedCache
func NewNamespaceScopedCache() *NamespaceScopedCache {
	return &NamespaceScopedCache{
		Namespaces: make(map[string]GvkToInformers),
		started:    false,
	}
}

// Get will attempt to get a Kubernetes resource from the cache for the
// provided key and GVK. The general flow of this function is:
// - If an informer doesn't exist that can facilitate finding a resource
// based on the provided key & GVK an InformerNotFoundErr will be returned
// - Every Informer for a Namespace-GVK pair will be queried.
// -- If the requested resource is found it will be returned
// -- If the requested resource is not found a NotFound error will be returned
func (nsc *NamespaceScopedCache) Get(key types.NamespacedName, gvk schema.GroupVersionKind) (runtime.Object, error) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	found := false
	var obj runtime.Object
	var err error

	// Check if any informers exist in the provided namespace
	if _, ok := nsc.Namespaces[key.Namespace]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist for namespace %q", key.Namespace))
	}

	// Check if any informers exist for the provided namespace-gvk pair
	if _, ok := nsc.Namespaces[key.Namespace][gvk]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist in namespace %q for GVK %q", key.Namespace, gvk))
	}

	// Loop through all informers and attempt to get the requested resource
	for _, si := range nsc.Namespaces[key.Namespace][gvk] {
		obj, err = si.Get(key.String())
		if err != nil {
			// ignore error because it *could* be found by another informer
			continue
		}
		found = true
		break
	}

	// If we found the requested resource, return it
	// otherwise return a NotFound error
	if found {
		return obj, nil
	} else {
		return nil, errors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}, "could not find the given resource")
	}
}

// List will attempt to get a list of Kubernetes resource from the cache for the
// provided ListOptions and GVK. The general flow of this function is:
// - If the provided ListOptions.Namespace != ""
// -- If an informer doesn't exist that can facilitate finding a list of resources
// based on the provided namespace & GVK an InformerNotFoundErr will be returned
// -- Every Informer for a Namespace-GVK pair will be queried and the results
// will be aggregated into a single list. Any errors encountered will be returned immediately.
// - If the provided ListOptions.Namespace == ""
// -- Every Namespace that has informers for the provided gvk will be queried and
// the results will be aggregated into a single list. Any errors encountered will be returned immediately.
// - The results will be deduplicated and returned.
func (nsc *NamespaceScopedCache) List(listOpts client.ListOptions, gvk schema.GroupVersionKind) ([]runtime.Object, error) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	retList := []runtime.Object{}
	if listOpts.Namespace != "" {
		if _, ok := nsc.Namespaces[listOpts.Namespace]; !ok {
			return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist for namespace %q", listOpts.Namespace))
		}

		if _, ok := nsc.Namespaces[listOpts.Namespace][gvk]; !ok {
			return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist in namespace %q for GVK %q", listOpts.Namespace, gvk))
		}

		for _, si := range nsc.Namespaces[listOpts.Namespace][gvk] {
			list, err := si.List(listOpts)
			if err != nil {
				// we should be able to list from all informers so in this case return the error
				return nil, err
			}

			// for each of the informers we need to append the list of objects to the return list
			retList = append(retList, list...)
		}
	} else {
		// if no namespace given, we want to list from ALL namespaces that we know of
		for ns := range nsc.Namespaces {
			// if the GVK doesn't exist in this namespace just skip it
			if _, ok := nsc.Namespaces[ns][gvk]; !ok {
				continue
			}

			for _, si := range nsc.Namespaces[ns][gvk] {
				list, err := si.List(listOpts)
				if err != nil {
					// we should be able to list from all informers so in this case return the error
					return nil, err
				}

				// for each of the informers we need to append the list of objects to the return list
				retList = append(retList, list...)
			}
		}
	}

	deduplicatedList, err := deduplicateList(retList)
	if err != nil {
		return nil, err
	}

	return deduplicatedList, nil
}

// AddInformer will add a new informer to the NamespaceScopedCache
// based on the informer options provided. The general flow of this
// function is:
// - Create a new ScopeInformer
// - Add the ScopeInformer to the cache based on the provided options
// - Set the WatchErrorHandler on the ScopeInformer to forcefully remove
// the ScopeInformer from the cache
// - If the NamespaceScopedCache has been started, start the ScopeInformer
func (nsc *NamespaceScopedCache) AddInformer(infOpts InformerOptions) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()

	// Create the ScopeInformer
	si := NewScopeInformer(infOpts.Informer)

	// Add necessary mappings to the cache
	if _, ok := nsc.Namespaces[infOpts.Namespace]; !ok {
		nsc.Namespaces[infOpts.Namespace] = make(GvkToInformers)
	}

	if _, ok := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk]; !ok {
		nsc.Namespaces[infOpts.Namespace][infOpts.Gvk] = make(Informers)
	}

	if _, ok := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]; !ok {
		si.AddDependent(infOpts.Dependent)
		nsc.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key] = si
	} else {
		si = nsc.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
		if !si.HasDependent(infOpts.Dependent) {
			si.AddDependent(infOpts.Dependent)
		}
	}

	// If permissions at any point don't allow this informer to run
	// remove it from the cache so it doesn't stick around
	removeFromCache := func() {
		nsc.RemoveInformer(infOpts, true)
	}
	_ = si.SetWatchErrorHandler(WatchErrorHandlerForScopeInformer(si, removeFromCache))

	// if the cache is already started, start the ScopeInformer
	if nsc.IsStarted() {
		go si.Run()
	}
}

// RemoveInformer will remove an informer from the NamespaceScopedCache
// based on the informer options provided. The general flow of this
// function is:
// - Get the ScopeInformer based on the provided options
// - Remove the provided Dependent from the ScopeInformer
// - If there are no more dependents for the ScopeInformer OR the removal is
// forced delete the informer from the cache and terminate it.
// - If there are no more informers for the given Namespace-GVK pair,
// remove it from the cache
// - If there are no more informers for the given Namespace,
// remove it from the cache
func (nsc *NamespaceScopedCache) RemoveInformer(infOpts InformerOptions, force bool) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()

	// Get the ScopeInformer based on the provided options
	si := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]

	// Remove the dependent resource
	si.RemoveDependent(infOpts.Dependent)

	// if there are no more dependents or we are forcefully removing this informer
	// then delete the ScopeInformer from the cache and terminate it.
	if len(si.GetDependents()) == 0 || force {
		delete(nsc.Namespaces[infOpts.Namespace][infOpts.Gvk], infOpts.Key)
		si.Terminate()
	}

	// if there are no more informers for this gvk - remove it from the cache
	if len(nsc.Namespaces[infOpts.Namespace][infOpts.Gvk]) == 0 {
		delete(nsc.Namespaces[infOpts.Namespace], infOpts.Gvk)
	}

	// if there are no more informers for this namespace - remove it from the cache
	if len(nsc.Namespaces[infOpts.Namespace]) == 0 {
		delete(nsc.Namespaces, infOpts.Namespace)
	}
}

// Start will start the cache and all it's informers
func (nsc *NamespaceScopedCache) Start() {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	for nsKey := range nsc.Namespaces {
		for gvkKey := range nsc.Namespaces[nsKey] {
			for _, si := range nsc.Namespaces[nsKey][gvkKey] {
				go si.Run()
			}
		}
	}
	nsc.started = true
}

// IsStarted returns whether or not the cache has been started
func (nsc *NamespaceScopedCache) IsStarted() bool {
	return nsc.started
}

// Synced returns whether or not the cache has synced
func (nsc *NamespaceScopedCache) Synced() bool {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	for nsKey := range nsc.Namespaces {
		for gvkKey := range nsc.Namespaces[nsKey] {
			for _, si := range nsc.Namespaces[nsKey][gvkKey] {
				if !si.HasSynced() {
					return false
				}
			}
		}
	}

	return true
}

// GvkHasInformer returns whether or not an informer
// exists in the cache for the provided InformerOptions
func (nsc *NamespaceScopedCache) GvkHasInformer(infOpts InformerOptions) bool {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	has := false
	if _, nsOk := nsc.Namespaces[infOpts.Namespace]; nsOk {
		if _, gvkOk := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk]; gvkOk {
			has = true
		}
	}

	return has
}

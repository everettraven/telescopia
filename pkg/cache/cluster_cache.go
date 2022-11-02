package cache

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClusterScopedCache is a dynamic cache
// with a focus on only tracking informers
// with watches at a cluster level
type ClusterScopedCache struct {
	GvkInformers GvkToInformers
	started      bool
	mu           sync.Mutex
}

// NewClusterScopedCache returns a new ClusterScopedCache
func NewClusterScopedCache() *ClusterScopedCache {
	return &ClusterScopedCache{
		GvkInformers: make(GvkToInformers),
		started:      false,
	}
}

// Get will attempt to get a Kubernetes resource from the cache for the
// provided key and GVK. The general flow of this function is:
// - If an informer doesn't exist that can facilitate finding a resource
// based on the GVK an InformerNotFoundErr will be returned
// - Every Informer for a GVK will be queried.
// -- If the requested resource is found it will be returned
// -- If the requested resource is not found a NotFound error will be returned
func (csc *ClusterScopedCache) Get(key types.NamespacedName, gvk schema.GroupVersionKind) (runtime.Object, error) {
	csc.mu.Lock()
	defer csc.mu.Unlock()
	found := false
	var obj runtime.Object
	var err error

	// Check if any informers exist for the provided gvk
	if _, ok := csc.GvkInformers[gvk]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("informer with cluster level watch not found for GVK %q", gvk))
	}

	// Loop through all informers and attempt to get the requested resource
	for _, si := range csc.GvkInformers[gvk] {
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
		return nil, fmt.Errorf("could not find the given resource")
	}
}

// List will attempt to get a list of Kubernetes resource from the cache for the
// provided ListOptions and GVK. The general flow of this function is:
// - If an informer doesn't exist that can facilitate finding a list of resources
// based on the provided GVK an InformerNotFoundErr will be returned
// - Every Informer for a GVK will be queried and the results
// will be aggregated into a single list. Any errors encountered will be returned immediately.
// - The results will be deduplicated and returned.
func (csc *ClusterScopedCache) List(listOpts client.ListOptions, gvk schema.GroupVersionKind) ([]runtime.Object, error) {
	csc.mu.Lock()
	defer csc.mu.Unlock()
	retList := []runtime.Object{}

	if _, ok := csc.GvkInformers[gvk]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("informer with cluster level watch not found for GVK %q", gvk))
	}

	for _, si := range csc.GvkInformers[gvk] {
		list, err := si.List(listOpts)
		if err != nil {
			// we should be able to list from all informers so in this case return the error
			return nil, err
		}

		// for each of the informers we need to append the list of objects to the return list
		retList = append(retList, list...)
	}

	deduplicatedList, err := deduplicateList(retList)
	if err != nil {
		return nil, err
	}

	return deduplicatedList, nil
}

// AddInformer will add a new informer to the ClusterScopedCache
// based on the informer options provided. The general flow of this
// function is:
// - Create a new ScopeInformer
// - Add the ScopeInformer to the cache based on the provided options
// - Set the WatchErrorHandler on the ScopeInformer to forcefully remove
// the ScopeInformer from the cache
// - If the ClusterScopedCache has been started, start the ScopeInformer
func (csc *ClusterScopedCache) AddInformer(infOpts InformerOptions) {
	csc.mu.Lock()
	defer csc.mu.Unlock()

	// Create the ScopeInformer
	si := NewScopeInformer(infOpts.Informer)

	// Add necessary mappings to the cache
	if _, ok := csc.GvkInformers[infOpts.Gvk]; !ok {
		csc.GvkInformers[infOpts.Gvk] = make(Informers)
	}

	if _, ok := csc.GvkInformers[infOpts.Gvk][infOpts.Key]; !ok {
		si.AddDependent(infOpts.Dependent)
		csc.GvkInformers[infOpts.Gvk][infOpts.Key] = si
	} else {
		si = csc.GvkInformers[infOpts.Gvk][infOpts.Key]
		if !si.HasDependent(infOpts.Dependent) {
			si.AddDependent(infOpts.Dependent)
		}
	}

	// If permissions at any point don't allow this informer to run
	// remove it from the cache so it doesn't stick around
	removeFromCache := func() {
		csc.RemoveInformer(infOpts, true)
	}
	_ = si.SetWatchErrorHandler(WatchErrorHandlerForScopeInformer(si, removeFromCache))

	// if the cache is already started, start the ScopeInformer
	if csc.IsStarted() {
		go si.Run()
	}
}

// RemoveInformer will remove an informer from the ClusterScopedCache
// based on the informer options provided. The general flow of this
// function is:
// - Get the ScopeInformer based on the provided options
// - Remove the provided Dependent from the ScopeInformer
// - If there are no more dependents for the ScopeInformer OR the removal is
// forced delete the informer from the cache and terminate it.
// - If there are no more informers for the given GVK ,
// remove it from the cache
func (csc *ClusterScopedCache) RemoveInformer(infOpts InformerOptions, force bool) {
	csc.mu.Lock()
	defer csc.mu.Unlock()

	// Get the ScopeInformer based on the provided options
	si := csc.GvkInformers[infOpts.Gvk][infOpts.Key]

	// Remove the dependent resource
	si.RemoveDependent(infOpts.Dependent)

	// if there are no more dependents or we are forcefully removing this informer
	// then delete the ScopeInformer from the cache and terminate it.
	if len(si.GetDependents()) == 0 || force {
		delete(csc.GvkInformers[infOpts.Gvk], infOpts.Key)
		si.Terminate()
	}

	// if there are no more informers for this gvk - remove it from the cache
	if len(csc.GvkInformers[infOpts.Gvk]) == 0 {
		delete(csc.GvkInformers, infOpts.Gvk)
	}
}

// Start will start the cache and all it's informers
func (csc *ClusterScopedCache) Start() {
	csc.mu.Lock()
	defer csc.mu.Unlock()
	for gvkKey := range csc.GvkInformers {
		for _, si := range csc.GvkInformers[gvkKey] {
			go si.Run()
		}
	}

	csc.started = true
}

// IsStarted returns whether or not the cache has been started
func (csc *ClusterScopedCache) IsStarted() bool {
	return csc.started
}

// Synced returns whether or not the cache has synced
func (csc *ClusterScopedCache) Synced() bool {
	csc.mu.Lock()
	defer csc.mu.Unlock()
	for gvkKey := range csc.GvkInformers {
		for _, si := range csc.GvkInformers[gvkKey] {
			if !si.HasSynced() {
				return false
			}
		}
	}

	return true
}

// GvkHasInformer returns whether or not an informer
// exists in the cache for the provided InformerOptions
func (csc *ClusterScopedCache) GvkHasInformer(infOpts InformerOptions) bool {
	csc.mu.Lock()
	defer csc.mu.Unlock()
	_, ok := csc.GvkInformers[infOpts.Gvk]
	return ok
}

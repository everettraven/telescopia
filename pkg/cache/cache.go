package cache

import (
	"context"
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// ScopeInformerFactory is a function that is used to create a
// ScopeInformer for the provided GVR and SharedInformerOptions.
// It is up to the function to properly implement any error
// handling logic for the informer losing permissions.
type ScopeInformerFactory func(gvr schema.GroupVersionResource, options ...informers.SharedInformerOption) (*ScopeInformer, error)

// ScopedCache is a wrapper around the
// NamespaceScopedCache and ClusterScopedCache
// that implements the controller-runtime
// cache.Cache interface. Wrapping both scoped
// caches enables this cache to dynamically handle
// informers that establish watches at both the cluster
// and namespace level
type ScopedCache struct {
	// nsCache is the NamespaceScopedCache
	// being wrapped by the ScopedCache
	nsCache *NamespaceScopedCache
	// clusterCache is the ClusterScopedCache
	// being wrapped by the ScopedCache
	clusterCache *ClusterScopedCache
	// RESTMapper is used when determining
	// if an API is namespaced or not
	RESTMapper apimeta.RESTMapper
	// Scheme is used when determining
	// if an API is namespaced or not
	Scheme *runtime.Scheme
	// started represents whether or not
	// the ScopedCache has been started
	started bool
	// scopeInformerFactory is used
	// to create ScopedInformers whenever
	// one needs to be created by the
	// ScopedCache.
	scopeInformerFactory ScopeInformerFactory
}

// ScopedCacheOption is a function to set values on the ScopedCache
type ScopedCacheOption func(*ScopedCache)

// WithScopedInformerFactory is an option that can be used
// to set the ScopedCache.scopedInformerFactory field when
// creating a new ScopedCache.
func WithScopedInformerFactory(sif ScopeInformerFactory) ScopedCacheOption {
	return func(sc *ScopedCache) {
		sc.scopeInformerFactory = sif
	}
}

func ScopedCacheBuilder(scOpts ...ScopedCacheOption) cache.NewCacheFunc {
	return func(config *rest.Config, opts cache.Options) (cache.Cache, error) {
		opts, err := defaultOpts(config, opts)
		if err != nil {
			return nil, err
		}

		namespacedCache := NewNamespaceScopedCache()
		clusterCache := NewClusterScopedCache()

		sc := &ScopedCache{nsCache: namespacedCache, Scheme: opts.Scheme, RESTMapper: opts.Mapper, clusterCache: clusterCache}
		for _, opt := range scOpts {
			opt(sc)
		}

		return sc, nil
	}
}

// client.Reader implementation
// ----------------------

func (sc *ScopedCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	var fetchObj runtime.Object
	isNamespaced, err := IsAPINamespaced(obj, sc.Scheme, sc.RESTMapper)
	if err != nil {
		return err
	}

	// obj could have an empty GVK, lets make sure we have a proper gvk
	gvk, err := apiutil.GVKForObject(obj, sc.Scheme)
	if err != nil {
		return fmt.Errorf("encountered an error getting GVK for object: %w", err)
	}

	_, gvkClusterScoped := sc.clusterCache.GvkInformers[gvk]

	if !isNamespaced || gvkClusterScoped || len(sc.nsCache.Namespaces) == 0 {
		fetchObj, err = sc.clusterCache.Get(key, gvk)
		if err != nil {
			return err
		}
	} else {
		fetchObj, err = sc.nsCache.Get(key, gvk)
		if err != nil {
			return err
		}
	}

	obj = fetchObj.(client.Object)
	return nil
}

func (sc *ScopedCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	listOpts := client.ListOptions{}
	listOpts.ApplyOptions(opts)

	var objs []runtime.Object

	isNamespaced, err := IsAPINamespaced(list, sc.Scheme, sc.RESTMapper)
	if err != nil {
		return err
	}

	// obj could have an empty GVK, lets make sure we have a proper gvk
	gvk, err := apiutil.GVKForObject(list, sc.Scheme)
	if err != nil {
		return fmt.Errorf("encountered an error getting GVK for object: %w", err)
	}

	gvkForListItems := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    strings.TrimSuffix(gvk.Kind, "List"),
	}

	_, gvkClusterScoped := sc.clusterCache.GvkInformers[gvkForListItems]

	allItems, err := apimeta.ExtractList(list)
	if err != nil {
		return err
	}

	if !isNamespaced || gvkClusterScoped || len(sc.nsCache.Namespaces) == 0 {
		// Look at the global cache to get the objects with the specified GVK
		objs, err = sc.clusterCache.List(listOpts, gvkForListItems)
		if err != nil {
			return err
		}
	} else {
		objs, err = sc.nsCache.List(listOpts, gvkForListItems)
		if err != nil {
			return err
		}
	}

	allItems = append(allItems, objs...)

	allItems, err = deduplicateList(allItems)
	if err != nil {
		return fmt.Errorf("encountered an error attempting to list: %w", err)
	}

	return apimeta.SetList(list, allItems)
}

// deduplicateList is meant to remove duplicate objects from a list of objects
func deduplicateList(objs []runtime.Object) ([]runtime.Object, error) {
	uidMap := make(map[types.UID]struct{})
	objList := []runtime.Object{}

	for _, obj := range objs {
		// turn runtime.Object to a client.Object so we can get the resource UID
		crObj, ok := obj.(client.Object)
		if !ok {
			return nil, fmt.Errorf("could not convert list item to client.Object")
		}
		// if the UID of the resource is not already in the map then it is a new object
		// and can be added to the list.
		if _, ok := uidMap[crObj.GetUID()]; !ok {
			objList = append(objList, obj)
		}
	}

	return objList, nil
}

// ----------------------

// cache.Informers implementation
// ----------------------

func (sc *ScopedCache) GetInformer(ctx context.Context, obj client.Object) (cache.Informer, error) {
	mapping, err := sc.RESTMapper.RESTMapping(obj.GetObjectKind().GroupVersionKind().GroupKind())
	if err != nil {
		return nil, err
	}

	// TODO: Figure out the best way to add the created informer to the corresponding cache
	// Question - Does this only ever get called for cluster scoped?

	if obj.GetNamespace() != v1.NamespaceAll {
		return sc.scopeInformerFactory(mapping.Resource, informers.WithNamespace(obj.GetNamespace()))
	}

	return sc.scopeInformerFactory(mapping.Resource)
}

func (sc *ScopedCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (cache.Informer, error) {
	mapping, err := sc.RESTMapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return nil, err
	}

	// TODO: Figure out the best way to add the created informer to the cache
	// Question - Does this only ever get called for cluster scoped?

	return sc.scopeInformerFactory(mapping.Resource)
}

func (sc *ScopedCache) Start(ctx context.Context) error {
	sc.clusterCache.Start()
	sc.nsCache.Start()

	sc.started = true

	return nil
}

func (sc *ScopedCache) WaitForCacheSync(ctx context.Context) bool {
	// TODO: figure out how to properly implement this with the new layered approach
	return true
}

func (sc *ScopedCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// TODO: figure out how to properly implement this with the new layered approach
	return fmt.Errorf("UNIMPLEMENTED")
}

// ----------------------

// Custom functions for ScopedCache

func (sc *ScopedCache) AddInformer(infOpts InformerOptions) {
	if infOpts.Namespace != "" {
		sc.nsCache.AddInformer(infOpts)
	} else {
		sc.clusterCache.AddInformer(infOpts)
	}
}

func (sc *ScopedCache) RemoveInformer(infOpts InformerOptions) {
	if infOpts.Namespace != "" {
		sc.nsCache.RemoveInformer(infOpts)
	} else {
		sc.clusterCache.RemoveInformer(infOpts)
	}
}

// --------------------------------

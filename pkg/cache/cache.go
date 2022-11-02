package cache

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// ScopeInformerFactory is a function that is used to create a
// informers.GenericInformer for the provided GVR and SharedInformerOptions.
type ScopeInformerFactory func(gvr schema.GroupVersionResource, options ...informers.SharedInformerOption) (informers.GenericInformer, error)

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

	// cli is used to create SSARs to
	// check whether or not a request is
	// permitted before submitting it
	cli dynamic.Interface
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

// ScopeCacheBuilder is a builder function that
// can be used to return a controller-runtime
// cache.NewCacheFunc. This function enables controller-runtime
// to properly create a new ScopedCache
func ScopedCacheBuilder(scOpts ...ScopedCacheOption) crcache.NewCacheFunc {
	return func(config *rest.Config, opts crcache.Options) (crcache.Cache, error) {
		opts, err := defaultOpts(config, opts)
		if err != nil {
			return nil, err
		}

		namespacedCache := NewNamespaceScopedCache()
		clusterCache := NewClusterScopedCache()

		cli := dynamic.NewForConfigOrDie(config)

		sc := &ScopedCache{nsCache: namespacedCache, Scheme: opts.Scheme, RESTMapper: opts.Mapper, clusterCache: clusterCache, cli: cli}
		for _, opt := range scOpts {
			opt(sc)
		}

		return sc, nil
	}
}

// client.Reader implementation
// ----------------------

// Get will attempt to get the requested resource from the appropriate cache.
// The general flow for this function is:
// - Get the GVK for the provided object
// - Check if the proper permissions exist to perform this request
// - Get the requested resource from the appropriate cache, returning any errors
// -- If the request is not permitted:
// --- Any informers that meet the invalid permissions are forcefully removed from the cache
// --- A Forbidden error is returned
// TODO: Should we automatically create the informer if one is not found?
func (sc *ScopedCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object) error {
	var fetchObj runtime.Object

	// obj could have an empty GVK, lets make sure we have a proper gvk
	gvk, err := apiutil.GVKForObject(obj, sc.Scheme)
	if err != nil {
		return fmt.Errorf("getting GVK for object: %w", err)
	}

	// Check if the request is permitted
	mapping, err := sc.RESTMapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return err
	}

	permitted, err := canVerbResource(sc.cli, mapping.Resource, "get", key.Namespace)
	if err != nil {
		return fmt.Errorf("checking \"get\" permissions for resource: %w", err)
	}

	_, gvkClusterScoped := sc.clusterCache.GvkInformers[gvk]

	if gvkClusterScoped {
		// Return permission denied error if request is not permitted
		if !permitted {
			// remove the informers in the cluster cache
			for infKey := range sc.clusterCache.GvkInformers[gvk] {
				infOpt := InformerOptions{
					Gvk:       gvk,
					Key:       infKey,
					Dependent: &corev1.Namespace{},
				}

				// forcefully remove because permissions no longer exist
				sc.RemoveInformer(infOpt, true)
			}
			return errors.NewForbidden(mapping.Resource.GroupResource(), "", fmt.Errorf("not permitted"))
		}

		fetchObj, err = sc.clusterCache.Get(key, gvk)
		if err != nil {
			return err
		}
	} else {
		// Return permission denied error if request is not permitted
		if !permitted {
			// remove the informers in the cluster cache
			for infKey := range sc.nsCache.Namespaces[key.Namespace][gvk] {
				infOpt := InformerOptions{
					Namespace: key.Namespace,
					Gvk:       gvk,
					Key:       infKey,
					Dependent: &corev1.Namespace{},
				}
				// forcefully remove because permissions no longer exist
				sc.RemoveInformer(infOpt, true)
			}
			return errors.NewForbidden(mapping.Resource.GroupResource(), "", fmt.Errorf("not permitted"))
		}

		fetchObj, err = sc.nsCache.Get(key, gvk)
		if err != nil {
			return err
		}
	}

	outVal := reflect.ValueOf(obj)
	objVal := reflect.ValueOf(fetchObj)
	if !objVal.Type().AssignableTo(outVal.Type()) {
		return fmt.Errorf("cache had type %s, but %s was asked for", objVal.Type(), outVal.Type())
	}
	reflect.Indirect(outVal).Set(reflect.Indirect(objVal))

	return nil
}

// List will attempt to get the requested list of resources from the appropriate cache.
// The general flow for this function is:
// - Get the GVK
// - Check if the proper permissions exist to perform this request
// - Get the requested resource list from the appropriate cache, returning any errors
// -- If the request is not permitted:
// --- Any informers that meet the invalid permissions are forcefully removed from the cache
// --- A Forbidden error is returned
func (sc *ScopedCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	listOpts := client.ListOptions{}
	listOpts.ApplyOptions(opts)

	var objs []runtime.Object

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

	// Check if the request is permitted
	mapping, err := sc.RESTMapper.RESTMapping(gvkForListItems.GroupKind())
	if err != nil {
		return err
	}

	permitted, err := canVerbResource(sc.cli, mapping.Resource, "list", listOpts.Namespace)
	if err != nil {
		return fmt.Errorf("checking \"list\" permissions for resource: %w", err)
	}

	_, gvkClusterScoped := sc.clusterCache.GvkInformers[gvkForListItems]

	allItems, err := apimeta.ExtractList(list)
	if err != nil {
		return err
	}

	if gvkClusterScoped {
		// Return permission denied error if request is not permitted
		if !permitted {
			// remove the informers in the cluster cache
			for key := range sc.clusterCache.GvkInformers[gvk] {
				infOpt := InformerOptions{
					Gvk:       gvk,
					Key:       key,
					Dependent: &corev1.Namespace{},
				}

				// forcefully remove because permissions no longer exist
				sc.RemoveInformer(infOpt, true)
			}
			return errors.NewForbidden(mapping.Resource.GroupResource(), "", fmt.Errorf("not permitted"))
		}
		// Look at the global cache to get the objects with the specified GVK
		objs, err = sc.clusterCache.List(listOpts, gvkForListItems)
		if err != nil {
			return err
		}
	} else {
		// Return permission denied error if request is not permitted
		if !permitted {
			// remove the informers in the cluster cache
			for key := range sc.nsCache.Namespaces[listOpts.Namespace][gvk] {
				infOpt := InformerOptions{
					Namespace: listOpts.Namespace,
					Gvk:       gvk,
					Key:       key,
					Dependent: &corev1.Namespace{},
				}

				// forcefully remove because permissions no longer exist
				sc.RemoveInformer(infOpt, true)
			}
			return errors.NewForbidden(mapping.Resource.GroupResource(), "", fmt.Errorf("not permitted"))
		}
		objs, err = sc.nsCache.List(listOpts, gvkForListItems)
		if err != nil {
			return err
		}
	}

	allItems = append(allItems, objs...)

	return apimeta.SetList(list, allItems)
}

// deduplicateList is meant to remove duplicate objects from a list of objects
func deduplicateList(objs []runtime.Object) ([]runtime.Object, error) {
	uidMap := make(map[types.UID]struct{})
	objList := []runtime.Object{}

	for _, obj := range objs {
		cpObj := obj.DeepCopyObject()
		// turn runtime.Object to a client.Object so we can get the resource UID
		crObj, ok := cpObj.(client.Object)
		if !ok {
			return nil, fmt.Errorf("could not convert list item to client.Object")
		}
		// if the UID of the resource is not already in the map then it is a new object
		// and can be added to the list.
		if _, ok := uidMap[crObj.GetUID()]; !ok {
			objList = append(objList, cpObj)
		}
	}

	return objList, nil
}

// ----------------------

// cache.Informers implementation
// ----------------------

// GetInformer will attempt to get an informer for the provided object
// and add it to the cache. The general flow for this function is:
// - Get the GVK
// - Create InformerOptions
// - If the provided object has a Namespace value != "":
// -- If an informer doesn't already exist in the namespace cache:
// --- Generate a new informer scoped to the namespace
// --- Add the informer to the cache
// --- Return the new ScopeInformer
// -- If the informer already exists in the namespace cache:
// --- Return the existing ScopeInformer
// - If the provided object has a Namespace value == "":
// -- If an informer doesn't already exist in the cluster cache:
// --- Generate a new informer scoped to the cluster
// --- Add the informer to the cache
// --- Return the new ScopeInformer
// -- If the informer already exists in the cluster cache:
// --- Return the existing ScopeInformer
func (sc *ScopedCache) GetInformer(ctx context.Context, obj client.Object) (crcache.Informer, error) {
	// obj could have an empty GVK, lets make sure we have a proper gvk
	gvk, err := apiutil.GVKForObject(obj, sc.Scheme)
	if err != nil {
		return nil, fmt.Errorf("encountered an error getting GVK for object: %w", err)
	}

	mapping, err := sc.RESTMapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return nil, err
	}

	// create the informer options
	infOpts := InformerOptions{
		Gvk:       gvk,
		Key:       fmt.Sprintf("scopedcache-getinformer-%s", hashObject(obj)),
		Dependent: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{UID: types.UID(hashObject(obj))}},
	}

	var inf informers.GenericInformer

	if obj.GetNamespace() != corev1.NamespaceAll {
		infOpts.Namespace = obj.GetNamespace()

		if si, ok := sc.nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]; !ok {
			inf, err = sc.scopeInformerFactory(mapping.Resource, informers.WithNamespace(obj.GetNamespace()))
			if err != nil {
				return nil, err
			}

			infOpts.Informer = inf
			sc.AddInformer(infOpts)
			return sc.nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key], nil
		} else {
			return si, nil
		}
	} else {
		if si, ok := sc.clusterCache.GvkInformers[infOpts.Gvk][infOpts.Key]; !ok {
			inf, err = sc.scopeInformerFactory(mapping.Resource)
			if err != nil {
				return nil, err
			}

			infOpts.Informer = inf
			sc.AddInformer(infOpts)
			return sc.clusterCache.GvkInformers[infOpts.Gvk][infOpts.Key], nil
		} else {
			return si, nil
		}
	}
}

// GetInformerForKind will attempt to get an informer for the provided GVK
// and add it to the cache. The general flow for this function is:
// - Get the GVK
// - Create InformerOptions:
// - If an informer doesn't already exist in the cluster cache:
// -- Generate a new informer scoped to the cluster
// -- Add the informer to the cache
// -- Return the new ScopeInformer
// - If the informer already exists in the cluster cache:
// -- Return the existing ScopeInformer
func (sc *ScopedCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (crcache.Informer, error) {
	mapping, err := sc.RESTMapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return nil, err
	}

	infOpts := InformerOptions{
		Gvk:       gvk,
		Key:       fmt.Sprintf("scopedcache-getinformer-%s", hashObject(gvk)),
		Dependent: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{UID: types.UID(hashObject(gvk))}},
	}

	if si, ok := sc.clusterCache.GvkInformers[infOpts.Gvk][infOpts.Key]; !ok {
		inf, err := sc.scopeInformerFactory(mapping.Resource)
		if err != nil {
			return nil, err
		}

		infOpts.Informer = inf
		sc.AddInformer(infOpts)
		return sc.clusterCache.GvkInformers[infOpts.Gvk][infOpts.Key], nil
	} else {
		return si, nil
	}
}

// Start will start the ScopedCache. This involves starting both
// the ClusterScopedCache and NamespaceScopedCache and all their informers
func (sc *ScopedCache) Start(ctx context.Context) error {
	sc.clusterCache.Start()
	sc.nsCache.Start()

	sc.started = true

	return nil
}

// WaitForCacheSync will block until all the caches have been synced
func (sc *ScopedCache) WaitForCacheSync(ctx context.Context) bool {
	synced := false
	for {
		synced = sc.clusterCache.Synced() && sc.nsCache.Synced()
		if !synced {
			continue
		}
		break
	}

	return synced
}

// IndexField will add an index field to the appropriate informers
// The general flow of this function is:
// - Get the GVK
// - Create an Indexer to add to the ScopeInformers
// - If the object has a Namespace value != "":
// -- Add the indexer to all informers in the namespace cache for the Namespace-GVK pair
// - If the object has a Namespace value == "":
// -- Add the indexer to all informers in the cluster cache for the GVK
func (sc *ScopedCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// obj could have an empty GVK, lets make sure we have a proper gvk
	gvk, err := apiutil.GVKForObject(obj, sc.Scheme)
	if err != nil {
		return fmt.Errorf("encountered an error getting GVK for object: %w", err)
	}

	indexFunc := func(objRaw interface{}) ([]string, error) {
		obj, isObj := objRaw.(client.Object)
		if !isObj {
			return nil, fmt.Errorf("object of type %T is not client.Object", objRaw)
		}
		return extractValue(obj), nil
	}

	indexer := cache.Indexers{fmt.Sprintf("field:%s", field): indexFunc}

	// for now, add the indexer to all the informers for a gvk
	if obj.GetNamespace() != corev1.NamespaceAll {
		for _, inf := range sc.nsCache.Namespaces[obj.GetNamespace()][gvk] {
			err := inf.AddIndexers(indexer)
			if err != nil {
				return err
			}
		}
	} else {
		for _, inf := range sc.clusterCache.GvkInformers[gvk] {
			err := inf.AddIndexers(indexer)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// ----------------------

// Custom functions for ScopedCache

// AddInformer will add an informer to the appropriate
// cache based on the informer options provided.
func (sc *ScopedCache) AddInformer(infOpts InformerOptions) {
	if infOpts.Namespace != "" {
		sc.nsCache.AddInformer(infOpts)
	} else {
		sc.clusterCache.AddInformer(infOpts)
	}
}

// RemoveInformer will remove an informer from the appropriate
// cache based on the informer options provided.
func (sc *ScopedCache) RemoveInformer(infOpts InformerOptions, force bool) {
	if infOpts.Namespace != "" {
		sc.nsCache.RemoveInformer(infOpts, force)
	} else {
		sc.clusterCache.RemoveInformer(infOpts, force)
	}
}

// GvkHasInformer returns whether or not an informer
// exists in the cache for the provided InformerOptions
func (sc *ScopedCache) GvkHasInformer(infOpts InformerOptions) bool {
	if infOpts.Namespace != "" {
		return sc.nsCache.GvkHasInformer(infOpts)
	} else {
		return sc.clusterCache.GvkHasInformer(infOpts)
	}
}

// --------------------------------

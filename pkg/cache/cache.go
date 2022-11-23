package cache

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/everettraven/telescopia/pkg/cache/cluster"
	"github.com/everettraven/telescopia/pkg/cache/components"
	"github.com/everettraven/telescopia/pkg/cache/namespace"
	"github.com/everettraven/telescopia/pkg/cache/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	nsCache *namespace.NamespaceScopedCache
	// clusterCache is the ClusterScopedCache
	// being wrapped by the ScopedCache
	clusterCache *cluster.ClusterScopedCache
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

// ScopeCacheBuilder is a builder function that
// can be used to return a controller-runtime
// cache.NewCacheFunc. This function enables controller-runtime
// to properly create a new ScopedCache
func ScopedCacheBuilder(scOpts ...ScopedCacheOption) crcache.NewCacheFunc {
	return func(config *rest.Config, opts crcache.Options) (crcache.Cache, error) {
		opts, err := util.DefaultOpts(config, opts)
		if err != nil {
			return nil, err
		}

		namespacedCache := namespace.NewNamespaceScopedCache()
		clusterCache := cluster.NewClusterScopedCache()

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

	permitted, err := util.CanVerbResource(sc.cli, mapping.Resource, "get", key.Namespace)
	if err != nil {
		return fmt.Errorf("checking \"get\" permissions for resource: %w", err)
	}

	_, gvkClusterScoped := sc.clusterCache.GvkInformers[gvk]

	if gvkClusterScoped {
		// Return permission denied error if request is not permitted
		if !permitted {
			// remove the informers in the cluster cache
			// TODO: Should we do this? If we remove the cluster scoped informers
			// they may not be as easily recreated
			// for infKey := range sc.clusterCache.GvkInformers[gvk] {
			// 	infOpt := InformerOptions{
			// 		Gvk:       gvk,
			// 		Key:       infKey,
			// 		Dependent: &corev1.Namespace{},
			// 	}

			// 	// forcefully remove because permissions no longer exist
			// 	sc.RemoveInformer(infOpt, true)
			// }
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
				infOpt := components.InformerOptions{
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

	permitted, err := util.CanVerbResource(sc.cli, mapping.Resource, "list", listOpts.Namespace)
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
			// 	// remove the informers in the cluster cache
			// // TODO: Should we do this? If we remove the cluster scoped informers
			// // they may not be as easily recreated
			// 	for key := range sc.clusterCache.GvkInformers[gvk] {
			// 		infOpt := InformerOptions{
			// 			Gvk:       gvk,
			// 			Key:       key,
			// 			Dependent: &corev1.Namespace{},
			// 		}

			// 		// forcefully remove because permissions no longer exist
			// 		sc.RemoveInformer(infOpt, true)
			// 	}
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
				infOpt := components.InformerOptions{
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
	infOpts := components.InformerOptions{
		Gvk: gvk,
		Key: fmt.Sprintf("scopedcache-getinformer-%s", util.HashObject(obj)),
		// TODO: We should probably create our own type that implements the
		// controller-runtime client.Object interface to use instead of an
		// arbitrary type (like Namespace in this case)
		Dependent: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{UID: types.UID(util.HashObject(obj))}},
	}

	var inf informers.GenericInformer

	sharedInfOpts := []informers.SharedInformerOption{}

	// if there are any labels, create informer with specific label selector
	if len(obj.GetLabels()) != 0 {
		selector := labels.SelectorFromSet(obj.GetLabels())
		sharedInfOpts = append(sharedInfOpts, informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.LabelSelector = selector.String()
		}))
	}

	if obj.GetNamespace() != corev1.NamespaceAll {
		sharedInfOpts = append(sharedInfOpts, informers.WithNamespace(obj.GetNamespace()))
		infOpts.Namespace = obj.GetNamespace()

		if si, ok := sc.nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]; !ok {
			inf, err = sc.scopeInformerFactory(mapping.Resource, sharedInfOpts...)
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
			inf, err = sc.scopeInformerFactory(mapping.Resource, sharedInfOpts...)
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

	infOpts := components.InformerOptions{
		Gvk:       gvk,
		Key:       fmt.Sprintf("scopedcache-getinformer-%s", util.HashObject(gvk)),
		Dependent: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{UID: types.UID(util.HashObject(gvk))}},
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
func (sc *ScopedCache) AddInformer(infOpts components.InformerOptions) {
	if infOpts.Namespace != "" {
		sc.nsCache.AddInformer(infOpts)
	} else {
		sc.clusterCache.AddInformer(infOpts)
	}
}

// RemoveInformer will remove an informer from the appropriate
// cache based on the informer options provided.
func (sc *ScopedCache) RemoveInformer(infOpts components.InformerOptions, force bool) {
	if infOpts.Namespace != "" {
		sc.nsCache.RemoveInformer(infOpts, force)
	} else {
		sc.clusterCache.RemoveInformer(infOpts, force)
	}
}

// GvkHasInformer returns whether or not an informer
// exists in the cache for the provided InformerOptions
func (sc *ScopedCache) HasInformer(infOpts components.InformerOptions) bool {
	if infOpts.Namespace != "" {
		return sc.nsCache.HasInformer(infOpts)
	} else {
		return sc.clusterCache.HasInformer(infOpts)
	}
}

// PrintCache is a temporary function to help show the state of the
// cache by printing it out.
func (sc *ScopedCache) PrintCache() {
	fmt.Println("")
	fmt.Println("")
	fmt.Println("# ScopedCache State")
	fmt.Println("------------------------------------------------")
	fmt.Println("")
	fmt.Println("## ScopedCache Cluster Cache State")
	for gvk := range sc.clusterCache.GvkInformers {
		infKeys := []string{}
		for infKey := range sc.clusterCache.GvkInformers[gvk] {
			infKeys = append(infKeys, infKey)
		}

		fmt.Println(fmt.Sprintf("\t%s --> %s", gvk, infKeys))
	}

	fmt.Println("")
	fmt.Println("## ScopedCache Namespace Cache State")
	for ns := range sc.nsCache.Namespaces {
		fmt.Println(fmt.Sprintf("\tNamespace %q", ns))
		for gvk := range sc.nsCache.Namespaces[ns] {
			infKeys := []string{}
			for infKey := range sc.nsCache.Namespaces[ns][gvk] {
				infKeys = append(infKeys, infKey)
			}

			fmt.Println(fmt.Sprintf("\t\t%s --> %s", gvk, infKeys))
		}
	}
	fmt.Println("")
	fmt.Println("------------------------------------------------")
	fmt.Println("")
	fmt.Println("")
}

// --------------------------------

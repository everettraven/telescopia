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

type NamespaceScopedCache struct {
	Namespaces map[string]GvkToInformers
	started    bool
	mu         sync.Mutex
}

func NewNamespaceScopedCache() *NamespaceScopedCache {
	return &NamespaceScopedCache{
		Namespaces: make(map[string]GvkToInformers),
		started:    false,
	}
}

func (nsc *NamespaceScopedCache) Get(key types.NamespacedName, gvk schema.GroupVersionKind) (runtime.Object, error) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	found := false
	var obj runtime.Object
	var err error

	if _, ok := nsc.Namespaces[key.Namespace]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist for namespace %q", key.Namespace))
	}

	if _, ok := nsc.Namespaces[key.Namespace][gvk]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("no informers at the namespace level exist in namespace %q for GVK %q", key.Namespace, gvk))
	}

	for _, si := range nsc.Namespaces[key.Namespace][gvk] {
		obj, err = si.Get(key.String())
		if err != nil {
			// ignore error because it *could* be found by another informer
			continue
		}
		found = true
	}

	if found {
		return obj, nil
	} else {
		return nil, errors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}, "could not find the given resource")
	}
}

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

func (nsc *NamespaceScopedCache) AddInformer(infOpts InformerOptions) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	if _, ok := nsc.Namespaces[infOpts.Namespace]; !ok {
		nsc.Namespaces[infOpts.Namespace] = make(GvkToInformers)
	}

	if _, ok := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk]; !ok {
		nsc.Namespaces[infOpts.Namespace][infOpts.Gvk] = make(Informers)
	}

	si := NewScopeInformer(infOpts.Informer)
	removeFromCache := func() {
		nsc.RemoveInformer(infOpts)
	}
	si.SetWatchErrorHandler(WatchErrorHandlerForScopeInformer(si, removeFromCache))
	if nsc.IsStarted() {
		go si.Run()
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

}

func (nsc *NamespaceScopedCache) RemoveInformer(infOpts InformerOptions) {
	nsc.mu.Lock()
	defer nsc.mu.Unlock()
	// remove the dependent resource from the informer
	si := nsc.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]

	si.RemoveDependent(infOpts.Dependent)

	if len(si.GetDependents()) == 0 {
		delete(nsc.Namespaces[infOpts.Namespace][infOpts.Gvk], infOpts.Key)
		si.Terminate()
	}
}

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

func (nsc *NamespaceScopedCache) IsStarted() bool {
	return nsc.started
}

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

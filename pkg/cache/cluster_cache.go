package cache

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterScopedCache struct {
	GvkInformers GvkToInformers
	started      bool
}

func NewClusterScopedCache() *ClusterScopedCache {
	return &ClusterScopedCache{
		GvkInformers: make(GvkToInformers),
		started:      false,
	}
}

func (csc *ClusterScopedCache) Get(key types.NamespacedName, gvk schema.GroupVersionKind) (runtime.Object, error) {
	found := false
	var obj runtime.Object
	var err error

	if _, ok := csc.GvkInformers[gvk]; !ok {
		return nil, NewInformerNotFoundErr(fmt.Errorf("informer with cluster level watch not found for GVK %q", gvk))
	}

	for _, si := range csc.GvkInformers[gvk] {
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
		return nil, fmt.Errorf("could not find the given resource")
	}
}

func (csc *ClusterScopedCache) List(listOpts client.ListOptions, gvk schema.GroupVersionKind) ([]runtime.Object, error) {
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

func (csc *ClusterScopedCache) AddInformer(infOpts InformerOptions) {
	if _, ok := csc.GvkInformers[infOpts.Gvk]; !ok {
		csc.GvkInformers[infOpts.Gvk] = make(Informers)
	}

	si := NewScopeInformer(infOpts.Informer)
	if csc.IsStarted() {
		go si.Run()
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
}

func (csc *ClusterScopedCache) RemoveInformer(infOpts InformerOptions) {
	// remove the dependent resource from the informer
	si := csc.GvkInformers[infOpts.Gvk][infOpts.Key]

	si.RemoveDependent(infOpts.Dependent)

	if len(si.GetDependents()) == 0 {
		delete(csc.GvkInformers[infOpts.Gvk], infOpts.Key)
		si.Terminate()
	}
}

func (csc *ClusterScopedCache) Start() {
	for gvkKey := range csc.GvkInformers {
		for _, si := range csc.GvkInformers[gvkKey] {
			go si.Run()
		}
	}

	csc.started = true
}

func (csc *ClusterScopedCache) IsStarted() bool {
	return csc.started
}

func (csc *ClusterScopedCache) Synced() bool {
	for gvkKey := range csc.GvkInformers {
		for _, si := range csc.GvkInformers[gvkKey] {
			if !si.HasSynced() {
				return false
			}
		}
	}

	return true
}

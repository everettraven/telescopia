package util

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"time"

	"github.com/davecgh/go-spew/spew"
	authv1 "k8s.io/api/authorization/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// Some default/helpful stuff
// --------------------------------
var defaultResyncTime = 10 * time.Hour

func DefaultOpts(config *rest.Config, opts cache.Options) (cache.Options, error) {
	// Use the default Kubernetes Scheme if unset
	if opts.Scheme == nil {
		opts.Scheme = scheme.Scheme
	}

	// Construct a new Mapper if unset
	if opts.Mapper == nil {
		var err error
		opts.Mapper, err = apiutil.NewDiscoveryRESTMapper(config)
		if err != nil {
			return opts, fmt.Errorf("could not create RESTMapper from config")
		}
	}

	// Default the resync period to 10 hours if unset
	if opts.Resync == nil {
		opts.Resync = &defaultResyncTime
	}
	return opts, nil
}

// IsAPINamespaced returns true if the object is namespace scoped.
// For unstructured objects the gvk is found from the object itself.
func IsAPINamespaced(obj runtime.Object, scheme *runtime.Scheme, restmapper apimeta.RESTMapper) (bool, error) {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return false, err
	}

	return IsAPINamespacedWithGVK(gvk, scheme, restmapper)
}

// IsAPINamespacedWithGVK returns true if the object having the provided
// GVK is namespace scoped.
func IsAPINamespacedWithGVK(gk schema.GroupVersionKind, scheme *runtime.Scheme, restmapper apimeta.RESTMapper) (bool, error) {
	restmapping, err := restmapper.RESTMapping(schema.GroupKind{Group: gk.Group, Kind: gk.Kind})
	if err != nil {
		return false, fmt.Errorf("failed to get restmapping: %w", err)
	}

	scope := restmapping.Scope.Name()

	if scope == "" {
		return false, errors.New("scope cannot be identified, empty scope returned")
	}

	if scope != apimeta.RESTScopeNameRoot {
		return true, nil
	}
	return false, nil
}

// createSSAR is a helper function to create the SelfSubjectAccessReview on cluster and return
// the resulting SelfSubjectAccessReview in the response.
func createSSAR(cli dynamic.Interface, ssar *authv1.SelfSubjectAccessReview) (*authv1.SelfSubjectAccessReview, error) {
	ssarUC, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ssar)
	if err != nil {
		return nil, fmt.Errorf("encountered an error converting to unstructured: %w", err)
	}

	uSSAR := &unstructured.Unstructured{}
	uSSAR.SetGroupVersionKind(ssar.GroupVersionKind())
	uSSAR.Object = ssarUC

	ssarClient := cli.Resource(authv1.SchemeGroupVersion.WithResource("selfsubjectaccessreviews"))
	uCreatedSSAR, err := ssarClient.Create(context.TODO(), uSSAR, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("encountered an error creating a cluster level SSAR: %w", err)
	}

	createdSSAR := &authv1.SelfSubjectAccessReview{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(uCreatedSSAR.UnstructuredContent(), createdSSAR)
	if err != nil {
		return nil, fmt.Errorf("encountered an error converting from unstructured: %w", err)
	}

	return createdSSAR, nil
}

// CanVerbResource will create a SelfSubjectAccessReview for a given resource, verb, and namespace and return whether or
// not a user/ServiceAccount has the permissions to "verb" (get, list, watch, etc.) the given resource in the given namespace.
// A namespace value of ""(empty) will result in checking permissions in all namespaces (cluster-scoped)
func CanVerbResource(cli dynamic.Interface, gvr schema.GroupVersionResource, verb string, namespace string) (bool, error) {
	// Check if we have cluster permissions to list the resource
	// create the cluster level SelfSubjectAccessReview
	ssar := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     gvr.Group,
				Version:   gvr.Version,
				Resource:  gvr.Resource,
			},
		},
	}

	createdSSAR, err := createSSAR(cli, ssar)
	if err != nil {
		return false, fmt.Errorf("encountered an error creating a cluster level SSAR: %w", err)
	}

	return createdSSAR.Status.Allowed, nil
}

// HashObject calculates a hash from an object
func HashObject(obj interface{}) string {
	hasher := fnv.New32a()
	deepHashObject(hasher, &obj)
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
func deepHashObject(hasher hash.Hash, objectToWrite interface{}) {
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	printer.Fprintf(hasher, "%#v", objectToWrite)
}

// DeduplicateList is meant to remove duplicate objects from a list of objects
func DeduplicateList(objs []runtime.Object) ([]runtime.Object, error) {
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

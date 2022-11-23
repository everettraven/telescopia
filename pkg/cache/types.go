package cache

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

// ScopeInformerFactory is a function that is used to create a
// informers.GenericInformer for the provided GVR and SharedInformerOptions.
type ScopeInformerFactory func(gvr schema.GroupVersionResource, options ...informers.SharedInformerOption) (informers.GenericInformer, error)

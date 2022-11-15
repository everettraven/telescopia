package components

import "k8s.io/apimachinery/pkg/runtime/schema"

// Informers is a mapping of a string
// (meant to be a unique string) to a ScopeInformer
type Informers map[string]*ScopeInformer

// GvkToInformers is a mapping of GVK to Informers
type GvkToInformers map[schema.GroupVersionKind]Informers

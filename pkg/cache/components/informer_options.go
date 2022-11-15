package components

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

// InformerOptions are meant to set options
// when adding an informer to a cache
type InformerOptions struct {
	// Namespace the informer is watching
	Namespace string
	// GVK the informer is watching
	Gvk schema.GroupVersionKind
	// Unique identifier for the informer
	Key string
	// The informer itself
	Informer informers.GenericInformer
	// The resource dependent on the informer
	Dependent metav1.Object
}

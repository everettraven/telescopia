package namespace

import (
	"fmt"
	"time"

	"github.com/everettraven/telescopia/pkg/cache/components"
	"github.com/everettraven/telescopia/pkg/cache/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NamespaceScopedCache Unit Tests", func() {
	var (
		nsCache         *NamespaceScopedCache
		informerFactory informers.SharedInformerFactory
		infOpts         components.InformerOptions
	)

	BeforeEach(func() {
		nsCache = NewNamespaceScopedCache()
		Expect(nsCache).ShouldNot(BeNil())

		informerFactory = informers.NewSharedInformerFactory(k8sClient, 10*time.Second)
		Expect(informerFactory).NotTo(BeNil())

		inf, err := informerFactory.ForResource(corev1.SchemeGroupVersion.WithResource("pods"))
		Expect(err).ShouldNot(HaveOccurred())
		Expect(inf).ShouldNot(BeNil())

		infOpts = components.InformerOptions{
			Namespace: "test-ns",
			Gvk:       corev1.SchemeGroupVersion.WithKind("Pod"),
			Key:       "test-informer",
			Informer:  inf,
			Dependent: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-dep", UID: types.UID("test-dep-uid")}},
		}
	})

	When("Adding an Informer with NamespaceScopedCache.AddInformer()", func() {
		It("Should create the mapping of Namespace --> GVK --> Informer Key --> ScopeInformer", func() {
			nsCache.AddInformer(infOpts)
			verifyMappings(nsCache, infOpts)

			infOpts.Gvk = corev1.SchemeGroupVersion.WithKind("ConfigMap")
			nsCache.AddInformer(infOpts)
			verifyMappings(nsCache, infOpts)

			Expect(len(nsCache.Namespaces[infOpts.Namespace])).Should(Equal(2))

			scopeInformer := nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
			Expect(scopeInformer).ShouldNot(BeNil())
			Expect(len(scopeInformer.GetDependents())).Should(Equal(1))
			Expect(scopeInformer.HasDependent(infOpts.Dependent)).Should(BeTrue())
		})

		It("Should update the ScopeInformer dependents mapping if the informer already exists but is created with a different InformerOptions.Dependent", func() {
			nsCache.AddInformer(infOpts)

			oldInfOpts := infOpts
			infOpts.Dependent.SetName("test-dep-2")
			infOpts.Dependent.SetUID(types.UID("test-dep-uid-2"))

			nsCache.AddInformer(infOpts)

			scopeInformer := nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
			Expect(scopeInformer).ShouldNot(BeNil())
			Expect(len(scopeInformer.GetDependents())).Should(Equal(2))
			Expect(scopeInformer.HasDependent(oldInfOpts.Dependent)).Should(BeTrue())
			Expect(scopeInformer.HasDependent(infOpts.Dependent)).Should(BeTrue())
		})

		It("Should not update the ScopeInformer dependents mapping if the informer already exists and is created with the same InformerOptions.Dependent", func() {
			// add the same informer twice
			nsCache.AddInformer(infOpts)
			nsCache.AddInformer(infOpts)

			scopeInformer := nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
			Expect(scopeInformer).ShouldNot(BeNil())
			Expect(len(scopeInformer.GetDependents())).Should(Equal(1))
			Expect(scopeInformer.HasDependent(infOpts.Dependent)).Should(BeTrue())
		})

		It("Should start the ScopeInformer if the NamespaceScopeCache has already been started", func() {
			nsCache.started = true
			nsCache.AddInformer(infOpts)

			scopeInformer := nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
			Expect(scopeInformer).ShouldNot(BeNil())

			started := func() bool {
				return scopeInformer.HasSynced()
			}
			Eventually(started).Should(BeTrue())

			// kill the informer so it doesn't cause problems stopping envtest
			scopeInformer.Terminate()
		})
	})

	When("Removing an Informer with NamespaceScopedCache.RemoveInformer()", func() {
		BeforeEach(func() {
			nsCache.AddInformer(infOpts)
		})

		It("Should just return without doing anything if the provided InformerOptions don't map to an existing Informer", func() {
			invalidInfOpts := components.InformerOptions{
				Namespace: "non-existent",
				Gvk:       corev1.SchemeGroupVersion.WithKind("Pod"),
				Key:       "non-existent",
			}

			nsCache.RemoveInformer(invalidInfOpts, false)
			verifyMappings(nsCache, infOpts)
		})

		It("Should remove the dependent from the ScopeInformer based on the InformerOptions provided", func() {
			oldInfOpts := infOpts

			// Create a second dependent
			infOpts.Dependent.SetName("test-dep-2")
			infOpts.Dependent.SetUID(types.UID("test-dep-uid-2"))
			nsCache.AddInformer(infOpts)

			// remove the informer
			nsCache.RemoveInformer(oldInfOpts, false)

			// the informer should still exist, just not have the dependent resource from the old informer options
			scopeInformer := nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key]
			Expect(scopeInformer).ShouldNot(BeNil())
			Expect(scopeInformer.HasDependent(oldInfOpts.Dependent)).Should(BeFalse())
		})

		It("Should remove the ScopeInformer from the cache if the ScopeInformer no longer has any dependents", func() {
			nsCache.RemoveInformer(infOpts, false)
			Expect(nsCache.Namespaces).Should(BeEmpty())
		})

		It("Should remove the ScopeInformer from the cache when it still has dependents if the force parameter is set to true", func() {
			oldInfOpts := infOpts

			// Create a second dependent
			infOpts.Dependent.SetName("test-dep-2")
			infOpts.Dependent.SetUID(types.UID("test-dep-uid-2"))
			nsCache.AddInformer(infOpts)

			// remove the informer
			nsCache.RemoveInformer(oldInfOpts, true)
			Expect(nsCache.Namespaces).Should(BeEmpty())
		})

		It("Should only remove the Informer with the key specified by the provided InformerOptions", func() {
			oldInfOpts := infOpts

			// Create a second Informer with a different key
			infOpts.Key = "differentKey"
			nsCache.AddInformer(infOpts)

			// remove the informer
			nsCache.RemoveInformer(oldInfOpts, true)
			verifyMappings(nsCache, infOpts)
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).ShouldNot(HaveKey(oldInfOpts.Key))
		})

		It("Should only remove the GVK and Informer specified by the provided InformerOptions", func() {
			oldInfOpts := infOpts

			// Create a second Informer with a different key & GVK
			infOpts.Key = "differentKey"
			infOpts.Gvk = appsv1.SchemeGroupVersion.WithKind("Deployment")
			nsCache.AddInformer(infOpts)

			// remove the informer
			nsCache.RemoveInformer(oldInfOpts, true)
			verifyMappings(nsCache, infOpts)
			Expect(nsCache.Namespaces[infOpts.Namespace]).ShouldNot(HaveKey(oldInfOpts.Gvk))
		})

		It("Should only remove the Namespace, GVK, and Informer specified by the provided InformerOptions", func() {
			oldInfOpts := infOpts

			// Create a second Informer with a different key & GVK
			infOpts.Key = "differentKey"
			infOpts.Gvk = appsv1.SchemeGroupVersion.WithKind("Deployment")
			infOpts.Namespace = "different-ns"
			nsCache.AddInformer(infOpts)

			// remove the informer
			nsCache.RemoveInformer(oldInfOpts, true)
			verifyMappings(nsCache, infOpts)
			Expect(nsCache.Namespaces).ShouldNot(HaveKey(oldInfOpts.Namespace))
		})
	})

	When("Checking if an Informer already exists in the NamespaceScopedCache with NamespaceScopedCache.HasInformer()", func() {
		BeforeEach(func() {
			nsCache.AddInformer(infOpts)
		})

		It("Should return false if the cache is empty", func() {
			// for this case only just create a new NamespaceScopedCache
			nsCache = NewNamespaceScopedCache()
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return false if the cache has a mapping for the namespace but not the GVK", func() {
			infOpts.Gvk = corev1.SchemeGroupVersion.WithKind("ConfigMap")
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return false if the cache has a mapping for the namespace and GVK but not the informer key", func() {
			infOpts.Key = "invalid"
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return true if the cache has a mapping for the namespace, GVK, and informer key", func() {
			Expect(nsCache.HasInformer(infOpts)).Should(BeTrue())
		})

		It("Should return false after using NamespaceScopedCache.RemoveInformer() with the same InformerOptions", func() {
			nsCache.RemoveInformer(infOpts, false)
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})
	})

	Context("Retrieving resources", func() {
		BeforeEach(func() {
			nsCache.AddInformer(infOpts)
			nsCache.Start()

			started := func() bool {
				return nsCache.Synced()
			}
			Eventually(started).Should(BeTrue())
		})

		AfterEach(func() {
			nsCache.Terminate()
		})
		When("Getting resources with NamespaceScopedCache.Get()", func() {
			It("Should return an InformerNotFoundErr if the provided Namespace doesn't exist in the cache", func() {
				obj, err := nsCache.Get(types.NamespacedName{Namespace: "noinformers", Name: "test"}, appsv1.SchemeGroupVersion.WithKind("Deployment"))
				Expect(obj).Should(BeNil())
				Expect(err).ShouldNot(BeNil())
				Expect(errors.IsInformerNotFoundErr(err)).Should(BeTrue())
				Expect(err.Error()).Should(Equal("no informers at the namespace level exist for namespace \"noinformers\""))
			})

			It("Should return an InformerNotFoundErr if no informers for the provided GVK are found in the Namespace", func() {
				gvk := appsv1.SchemeGroupVersion.WithKind("Deployment")
				ns := "test-ns"
				obj, err := nsCache.Get(types.NamespacedName{Namespace: ns, Name: "test"}, gvk)
				Expect(obj).Should(BeNil())
				Expect(err).ShouldNot(BeNil())
				Expect(errors.IsInformerNotFoundErr(err)).Should(BeTrue())
				Expect(err.Error()).Should(Equal(fmt.Sprintf("no informers at the namespace level exist in namespace %q for GVK %q", ns, gvk)))
			})

			It("Should return an error if the requested resource could not be found", func() {
				gvk := corev1.SchemeGroupVersion.WithKind("Pod")
				ns := "test-ns"
				obj, err := nsCache.Get(types.NamespacedName{Namespace: ns, Name: "test"}, gvk)
				Expect(obj).Should(BeNil())
				Expect(err).ShouldNot(BeNil())
				Expect(apierrors.IsNotFound(err)).Should(BeTrue())
			})

			It("Should return the runtime.Object if the resource could be found", func() {
				gvk := corev1.SchemeGroupVersion.WithKind("Pod")
				ns := "test-ns"
				obj, err := nsCache.Get(types.NamespacedName{Namespace: ns, Name: "test-pod-0"}, gvk)
				Expect(err).Should(BeNil())
				Expect(obj).ShouldNot(BeNil())
				pod, ok := obj.(*corev1.Pod)
				Expect(ok).Should(BeTrue())
				Expect(pod.Name).Should(Equal("test-pod-0"))
				Expect(pod.Namespace).Should(Equal(ns))
				Expect(len(pod.Spec.Containers)).Should(Equal(1))
				Expect(pod.Spec.Containers[0].Name).Should(Equal("nginx"))
				Expect(pod.Spec.Containers[0].Image).Should(Equal("nginx:1.14.2"))
			})
		})

		When("Listing resources with NamespaceScopedCache.List()", func() {
			It("Should return an InformerNotFoundError if ListOpts.Namespace doesn't exist in the cache", func() {
				objs, err := nsCache.List(client.ListOptions{Namespace: "noinformers"}, appsv1.SchemeGroupVersion.WithKind("Deployment"))
				Expect(objs).Should(BeNil())
				Expect(err).ShouldNot(BeNil())
				Expect(errors.IsInformerNotFoundErr(err)).Should(BeTrue())
				Expect(err.Error()).Should(Equal("no informers at the namespace level exist for namespace \"noinformers\""))
			})

			It("Should return an InformerNotFoundError if ListOpts.Namespace exists but there is no informer for the GVK", func() {
				gvk := appsv1.SchemeGroupVersion.WithKind("Deployment")
				ns := "test-ns"
				objs, err := nsCache.List(client.ListOptions{Namespace: ns}, gvk)
				Expect(objs).Should(BeNil())
				Expect(err).ShouldNot(BeNil())
				Expect(errors.IsInformerNotFoundErr(err)).Should(BeTrue())
				Expect(err.Error()).Should(Equal(fmt.Sprintf("no informers at the namespace level exist in namespace %q for GVK %q", ns, gvk)))
			})

			It("Should return a list of all requested resources from the requested Namespace", func() {
				gvk := corev1.SchemeGroupVersion.WithKind("Pod")
				ns := "test-ns"
				objs, err := nsCache.List(client.ListOptions{Namespace: ns}, gvk)
				Expect(err).Should(BeNil())
				Expect(objs).ShouldNot(BeNil())
				// Due to the nature of this test having to delete
				// and recreate resources we can't guarantee that there will be
				// an exact number of resources so we just make sure there are some
				Expect(len(objs)).Should(BeNumerically(">", 0))
				for _, obj := range objs {
					pod, ok := obj.(*corev1.Pod)
					Expect(ok).Should(BeTrue())
					Expect(pod.Name).Should(ContainSubstring("test-pod-"))
					Expect(pod.Namespace).Should(Equal(ns))
					Expect(len(pod.Spec.Containers)).Should(Equal(1))
					Expect(pod.Spec.Containers[0].Name).Should(Equal("nginx"))
					Expect(pod.Spec.Containers[0].Image).Should(Equal("nginx:1.14.2"))
				}
			})

			It("Should attempt to list from all namespaces in the cache that contain an informer for this GVK", func() {
				gvk := corev1.SchemeGroupVersion.WithKind("Pod")
				objs, err := nsCache.List(client.ListOptions{}, gvk)
				Expect(err).Should(BeNil())
				Expect(objs).ShouldNot(BeNil())
				// Due to the nature of this test having to delete
				// and recreate resources we can't guarantee that there will be
				// an exact number of resources so we just make sure there are some
				Expect(len(objs)).Should(BeNumerically(">", 0))
				for _, obj := range objs {
					// verify that each object is a Pod
					_, ok := obj.(*corev1.Pod)
					Expect(ok).Should(BeTrue())
				}
			})
		})
	})

})

// verifyMappings is a helper function to ensure
// that the proper mappings exist in the provided
// NamespaceScopedCache for the provided InformerOptions
func verifyMappings(nsCache *NamespaceScopedCache, infOpts components.InformerOptions) {
	Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
	Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
	Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))
}

package namespace

import (
	"time"

	"github.com/everettraven/telescopia/pkg/cache/components"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
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
			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))

			infOpts.Gvk = corev1.SchemeGroupVersion.WithKind("ConfigMap")
			nsCache.AddInformer(infOpts)
			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))

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

			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))
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
			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))
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
			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace]).ShouldNot(HaveKey(oldInfOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))
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
			Expect(nsCache.Namespaces).Should(HaveKey(infOpts.Namespace))
			Expect(nsCache.Namespaces).ShouldNot(HaveKey(oldInfOpts.Namespace))
			Expect(nsCache.Namespaces[infOpts.Namespace]).Should(HaveKey(infOpts.Gvk))
			Expect(nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]).Should(HaveKey(infOpts.Key))
		})
	})

	// TODO(everettraven): Add tests for the `HasInformer()` function
	When("Checking if an Informer already exists in the NamespaceScopedCache with NamespaceScopedCache.HasInformer()", func() {
		It("Should return false if the cache is empty", func() {
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return false if the cache has a mapping for the namespace but not the GVK", func() {
			nsCache.Namespaces[infOpts.Namespace] = make(components.GvkToInformers)
			nsCache.Namespaces[infOpts.Namespace][corev1.SchemeGroupVersion.WithKind("ConfigMap")] = make(components.Informers)
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return false if the cache has a mapping for the namespace and GVK but not the informer key", func() {
			nsCache.Namespaces[infOpts.Namespace] = make(components.GvkToInformers)
			nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk] = make(components.Informers)
			nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk]["invalid"] = components.NewScopeInformer(infOpts.Informer)
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})

		It("Should return true if the cache has a mapping for the namespace, GVK, and informer key", func() {
			nsCache.Namespaces[infOpts.Namespace] = make(components.GvkToInformers)
			nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk] = make(components.Informers)
			nsCache.Namespaces[infOpts.Namespace][infOpts.Gvk][infOpts.Key] = components.NewScopeInformer(infOpts.Informer)
			Expect(nsCache.HasInformer(infOpts)).Should(BeTrue())
		})

		It("Should return true after using NamespaceScopedCache.AddInformer() to add an with the same InformerOptions", func() {
			nsCache.AddInformer(infOpts)
			Expect(nsCache.HasInformer(infOpts)).Should(BeTrue())
		})

		It("Should return false after using NamespaceScopedCache.RemoveInformer() with the same InformerOptions", func() {
			nsCache.AddInformer(infOpts)
			Expect(nsCache.HasInformer(infOpts)).Should(BeTrue())
			nsCache.RemoveInformer(infOpts, false)
			Expect(nsCache.HasInformer(infOpts)).Should(BeFalse())
		})
	})
})

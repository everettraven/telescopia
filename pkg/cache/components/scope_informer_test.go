package components

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/everettraven/telescopia/pkg/cache/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ScopeInformer Unit Tests", func() {
	// create the informer factory
	When("Creating a new ScopeInformer", func() {
		var (
			si              *ScopeInformer
			ctx             context.Context
			cancel          context.CancelFunc
			informerFactory informers.SharedInformerFactory
		)

		BeforeEach(func() {
			informerFactory = informers.NewSharedInformerFactory(k8sClient, 10*time.Second)
			Expect(informerFactory).NotTo(BeNil())
			// Create a new informers.GenericInformer
			inf, err := informerFactory.ForResource(corev1.SchemeGroupVersion.WithResource("pods"))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(inf).ShouldNot(BeNil())

			// Expected context
			ctx, cancel = context.WithCancel(context.Background())

			// Create the new ScopeInformer
			si = NewScopeInformer(inf)
			Expect(si.informer).Should(Equal(inf))
			Expect(si.ctx).Should(BeEquivalentTo(ctx))
			Expect(si.cancel).ShouldNot(BeNil())
			Expect(si.dependents).ShouldNot(BeNil())
			Expect(si.eventListeners).ShouldNot(BeNil())
		})

		AfterEach(func() {
			cancel()
			si.Terminate()
		})

		When("Running the ScopeInformer", func() {
			It("Should start the ScopeInformer.informer", func() {
				// start running the ScopeInformer
				go si.Run()

				started := func() bool {
					return si.HasSynced()
				}

				Eventually(started).Should(BeTrue())
			})

			When("Terminating the ScopeInformer", func() {
				It("Should no longer be running", func() {
					si.Terminate()
					terminated := func() bool {
						return errors.Is(si.ctx.Err(), context.Canceled)
					}

					Eventually(terminated).Should(BeTrue())
				})
			})
		})

		When("Adding a Dependent", func() {
			var dependent v1.Object
			BeforeEach(func() {
				dependent = &corev1.Namespace{ObjectMeta: v1.ObjectMeta{UID: types.UID("test-dep"), Name: "test-ns"}}
				si.AddDependent(dependent)
			})

			It("Should be in the map returned by ScopeInformer.GetDependents()", func() {
				siDeps := si.GetDependents()
				Expect(siDeps).Should(HaveLen(1))
				Expect(siDeps).Should(HaveKeyWithValue(dependent.GetUID(), dependent))
			})

			It("Should have the dependent", func() {
				Expect(si.HasDependent(dependent)).Should(BeTrue())
			})

			When("Removing the Dependent", func() {
				BeforeEach(func() {
					si.RemoveDependent(dependent)
				})

				It("Should not have the dependent", func() {
					Expect(si.HasDependent(dependent)).Should(BeFalse())
				})
			})
		})

		When("Calling AddEventHandler()", func() {
			It("Should be added to the ScopeInformer.informer if it hasn't been before", func() {
				handler := cache.ResourceEventHandlerFuncs{}
				key := fmt.Sprintf("scopeinformer-addeventhandler-%s", util.HashObject(handler))

				si.AddEventHandler(handler)
				Expect(si.eventListeners).Should(HaveKeyWithValue(key, handler))
			})
		})

		When("Calling AddEventHandlerWithResyncPeriod()", func() {
			It("Should be added to the ScopeInformer.informer if it hasn't been before", func() {
				handler := cache.ResourceEventHandlerFuncs{}
				key := fmt.Sprintf("scopeinformer-addeventhandlerwithresyncperiod-%s", util.HashObject(handler))

				si.AddEventHandlerWithResyncPeriod(handler, 10*time.Second)
				Expect(si.eventListeners).Should(HaveKeyWithValue(key, handler))
			})
		})

		When("Setting the WatchErrorHandler", func() {
			It("Should add the WatchErrorHandler to the ScopeInformer.informer", func() {
				Expect(si.SetWatchErrorHandler(cache.DefaultWatchErrorHandler)).ShouldNot(HaveOccurred())
			})

			It("Should return an error if the informer is already started", func() {
				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				Expect(si.SetWatchErrorHandler(cache.DefaultWatchErrorHandler)).Should(HaveOccurred())
			})
		})

		When("Using the WatchErrorHandlerForScopeInformer helper", func() {
			It("Should call the removeCache function if error is of type Forbidden", func() {
				called := false
				rmCache := func() {
					called = true
				}

				WatchErrorHandlerForScopeInformer(si, rmCache)(nil, apierrors.NewForbidden(schema.GroupResource{}, "test", errors.New("test")))
				Expect(called).Should(BeTrue())
			})

			It("Should not call the removeCache function if error is not of type Forbidden", func() {
				called := false
				rmCache := func() {
					called = true
				}

				WatchErrorHandlerForScopeInformer(si, rmCache)(nil, errors.New("test"))
				Expect(called).Should(BeFalse())
			})
		})

		When("Adding indexers", func() {
			It("Should add the indexer to the ScopeInformer.informer", func() {
				Expect(si.AddIndexers(cache.Indexers{})).ShouldNot(HaveOccurred())
			})

			It("Should return an error if the informer is already started", func() {
				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				Expect(si.AddIndexers(cache.Indexers{})).Should(HaveOccurred())
			})
		})

		When("Getting a resource via the informer", func() {
			It("Should return an error if the resource does not exist", func() {
				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				_, err := si.Get(types.NamespacedName{Namespace: "test-ns", Name: "test-pod"}.String())
				Expect(apierrors.IsNotFound(err)).Should(BeTrue())
			})

			It("Should return the resource if it exists", func() {
				_, err := k8sClient.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: v1.ObjectMeta{Name: "test-ns"}}, v1.CreateOptions{})
				Expect(err).ShouldNot(HaveOccurred())

				expPod := &corev1.Pod{
					ObjectMeta: v1.ObjectMeta{
						Namespace: "test-ns",
						Name:      "test-pod",
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "nginx",
								Image: "nginx:1.14.2",
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 80,
									},
								},
							},
						},
					},
				}
				_, err = k8sClient.CoreV1().Pods("test-ns").Create(context.Background(), expPod, v1.CreateOptions{})
				Expect(err).ShouldNot(HaveOccurred())

				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				obj, err := si.Get(types.NamespacedName{Namespace: "test-ns", Name: "test-pod"}.String())
				Expect(err).ShouldNot(HaveOccurred())
				Expect(obj).ShouldNot(BeNil())

				actualPod, ok := obj.(*corev1.Pod)
				Expect(ok).Should(BeTrue())
				Expect(actualPod.Spec.Containers[0].Name).Should(BeEquivalentTo(expPod.Spec.Containers[0].Name))
				Expect(actualPod.Spec.Containers[0].Image).Should(BeEquivalentTo(expPod.Spec.Containers[0].Image))
				Expect(actualPod.Spec.Containers[0].Ports[0].ContainerPort).Should(BeEquivalentTo(expPod.Spec.Containers[0].Ports[0].ContainerPort))

				// Clean up the created resources
				err = k8sClient.CoreV1().Pods("test-ns").Delete(context.Background(), "test-pod", v1.DeleteOptions{})
				Expect(err).ShouldNot(HaveOccurred())

				err = k8sClient.CoreV1().Namespaces().Delete(context.Background(), "test-ns", v1.DeleteOptions{})
				Expect(err).ShouldNot(HaveOccurred())
			})
		})

		// TODO: Figure out why this is panicking
		When("Listing a resource via the informer", func() {
			It("Should list all pods in a specific namespace", func() {
				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				objs, err := si.List(client.ListOptions{Namespace: "test-ns-0"})
				Expect(err).ShouldNot(HaveOccurred())
				Expect(len(objs)).Should(Equal(5))
			})

			It("Should list all pods across the cluster", func() {
				go si.Run()
				started := func() bool {
					return si.HasSynced()
				}
				Eventually(started).Should(BeTrue())

				objs, err := si.List(client.ListOptions{})
				Expect(err).ShouldNot(HaveOccurred())
				Expect(len(objs)).Should(BeNumerically(">=", 10))
			})
		})
	})
})

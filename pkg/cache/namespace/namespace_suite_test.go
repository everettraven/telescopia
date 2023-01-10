package namespace

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	cfg       *rest.Config
	k8sClient kubernetes.Interface
	testEnv   *envtest.Environment
)

func TestComponents(t *testing.T) {
	RegisterFailHandler(Fail)
	SetDefaultEventuallyTimeout(1 * time.Minute)
	SetDefaultEventuallyPollingInterval(1 * time.Second)
	RunSpecs(t, "Namespace Cache Suite")
}

var _ = BeforeSuite(func() {
	scheme := runtime.NewScheme()
	err := corev1.AddToScheme(scheme)
	Expect(err).Should(BeNil())

	testEnv = &envtest.Environment{
		Scheme: scheme,
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
	// create the test resources
	createResources()
})

var _ = AfterSuite(func() {
	// clean up the test namespace
	deleteResources()
	Expect(testEnv.Stop()).ShouldNot(HaveOccurred())
})

func createResources() {
	namespace := "test-ns"
	createNs := func() error {
		_, err := k8sClient.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: v1.ObjectMeta{Name: namespace}}, v1.CreateOptions{})
		return err
	}
	Eventually(createNs).ShouldNot(HaveOccurred())
	// initialize a namespace named "test-ns"
	// with 5 pods named in the pattern "test-pod-#"
	for i := 0; i < 5; i++ {
		expPod := &corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Namespace: namespace,
				Name:      fmt.Sprintf("test-pod-%d", i),
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
		createPod := func() error {
			_, err := k8sClient.CoreV1().Pods(namespace).Create(context.Background(), expPod, v1.CreateOptions{})
			return err
		}
		Eventually(createPod).ShouldNot(HaveOccurred())
	}
}

func deleteResources() {
	// initialize a namespace named "test-ns"
	// with 5 pods named in the pattern "test-pod-#"
	namespace := "test-ns"
	for i := 0; i < 5; i++ {
		Expect(k8sClient.CoreV1().Pods(namespace).Delete(context.Background(), fmt.Sprintf("test-pod-%d", i), metav1.DeleteOptions{})).ShouldNot(HaveOccurred())
	}

	Expect(k8sClient.CoreV1().Namespaces().Delete(context.Background(), namespace, v1.DeleteOptions{}))
}

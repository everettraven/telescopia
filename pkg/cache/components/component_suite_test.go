package components

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	RunSpecs(t, "Components Suite")
}

var _ = BeforeSuite(func() {
	testEnv = &envtest.Environment{}

	var err error

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// initialize 2 namespaces with 5 pods each
	// namespaces are named following the pattern `test-ns-#`
	// pods are named following the pattern `test-pod-#`
	for i := 0; i < 2; i++ {
		namespace := fmt.Sprintf("test-ns-%d", i)
		createNs := func() error {
			_, err := k8sClient.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{ObjectMeta: v1.ObjectMeta{Name: namespace}}, v1.CreateOptions{})
			return err
		}
		Eventually(createNs).ShouldNot(HaveOccurred())

		for j := 0; j < 5; j++ {
			expPod := &corev1.Pod{
				ObjectMeta: v1.ObjectMeta{
					Namespace: namespace,
					Name:      fmt.Sprintf("test-pod-%d", j),
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
})

var _ = AfterSuite(func() {
	Expect(testEnv.Stop()).ShouldNot(HaveOccurred())
})

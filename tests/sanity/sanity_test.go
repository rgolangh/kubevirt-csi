package sanity_test

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kubevirt/csi-driver/pkg/generated"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	. "github.com/kubevirt/csi-driver/tests/common"
)

var infraCluster InfraCluster
var tenantCluster TenantCluster

var _ = BeforeSuite(func() {
	infraCluster = GetInfraCluster()
	PrepareEnv(infraCluster)
})

var _ = AfterSuite(func() {
	TearDownEnv(infraCluster)
})

var _ = Describe("KubeVirt CSI Driver sanity tests", func() {

	BeforeEach(func() {
		tenantCluster = GetTenantCluster(infraCluster)
	})

	Context("deployed on vanilla k8s", func() {

		It("Creates a pod with the sanity test", func() {
			deploySanityTest(infraCluster, tenantCluster)
		})
	})
})

func deploySanityTest(c InfraCluster, tenantSetup TenantCluster) {
	testNamespaceName := "kubevirt-csi-driver"
	testNamespace := v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespaceName,
		},
	}
	testNamespaceServiceAccount := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
	}
	tenantSetup.Client.CoreV1().Namespaces().Create(context.Background(), &testNamespace, metav1.CreateOptions{})
	tenantSetup.Client.CoreV1().ServiceAccounts(testNamespaceName).Create(context.Background(), &testNamespaceServiceAccount, metav1.CreateOptions{})

	assets := []string{
		"deploy/020-autorization.yaml",
		"deploy/sanity-test.yaml",
	}
	sort.Strings(assets)
	for _, a := range assets {
		fmt.Fprintf(GinkgoWriter, "Deploying to tenant %s\n", a)
		_, err := c.CreateObject(
			*tenantSetup.RestConfig,
			tenantSetup.Client.Discovery(),
			tenantSetup.Namespace,
			bytes.NewReader(generated.MustAsset(a)),
		)
		if !errors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("deploying %s should not have failed. Error %v", a, err))
		}
	}

	DeploySecretWithInfraDetails(c, tenantCluster)

	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   1.8,
		Steps:    13,
	}

	verifyVolumeFunc := func() (bool, error) {
		testPod, err := tenantSetup.Client.CoreV1().Pods(testNamespaceName).Get(context.Background(), "sanity-test", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		containerStatuses := testPod.Status.ContainerStatuses
		for _, containerStatus := range containerStatuses {
			if containerStatus.Name == "csi-sanity-test" && containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode == 0 {
				return true, nil
			}
		}
		return false, nil
	}

	wait.ExponentialBackoff(backoff, verifyVolumeFunc)
}

package functional_test

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubevirt/csi-driver/pkg/generated"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/yaml"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	clientwatch "k8s.io/client-go/tools/watch"
	kubevirtapiv1 "kubevirt.io/client-go/api/v1"

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

var _ = Describe("KubeVirt CSI Driver functional tests", func() {

	BeforeEach(func() {
		tenantCluster = GetTenantCluster(infraCluster)
	})

	Context("deployed on vanilla k8s", func() {

		It("Deploys the CSI driver components", func() {
			deployDriver(infraCluster, tenantCluster)
			DeploySecretWithInfraDetails(infraCluster, tenantCluster)
		})

		It("Deploys a pod consuming a PV by PVC", func() {
			deployStorageClass(tenantCluster)
			deployPVC(tenantCluster)
			// create pod check pod starts
			err := deployTestPod(tenantCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Destroys the test pod", func() {
			err := destroyTestPod(tenantCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Destroys the PVC", func() {
			// delete pvc, see disk is detached and deleted
			err := destroyPVC(tenantCluster)
			Expect(err).NotTo(HaveOccurred())
		})

		It("DV is unplugged from the VMI", func() {
			// check the infra DV is hot unplug
			_, err := watchDVUnpluged(&infraCluster)
			Expect(err).NotTo(HaveOccurred())
		})
		//todo test the dv is deleted from infracluster
	})
})

func deployDriver(c InfraCluster, tenantSetup TenantCluster) {
	// 1. create ServiceAccount on infra
	_, err := c.CreateObject(
		*c.VirtCli.Config(),
		c.VirtCli.Discovery(),
		c.Namespace,
		bytes.NewReader(generated.MustAsset("deploy/infra-cluster-service-account.yaml")))
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	// 2. create the rest of deploy/0xx-.yaml files
	assets := generated.AssetNames()
	sort.Strings(assets)
	for _, a := range assets {
		if !strings.HasPrefix(a, "deploy/0") {
			continue
		}
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
}

func destroyPVC(tenantSetup TenantCluster) error {
	pvc := v1.PersistentVolumeClaim{}
	pvcData := bytes.NewReader(generated.MustAsset("deploy/example/storage-claim.yaml"))
	err := yaml.NewYAMLToJSONDecoder(pvcData).Decode(&pvc)
	if err != nil {
		return err
	}
	return tenantSetup.Client.CoreV1().
		PersistentVolumeClaims(tenantSetup.Namespace).Delete(context.Background(), pvc.Name, metav1.DeleteOptions{})
}

func deployStorageClass(tenantSetup TenantCluster) {
	sc := storagev1.StorageClass{}
	storageClassData := bytes.NewReader(generated.MustAsset("deploy/example/storageclass.yaml"))
	err := yaml.NewYAMLToJSONDecoder(storageClassData).Decode(&sc)
	Expect(err).NotTo(HaveOccurred())
	_, err = tenantSetup.Client.StorageV1().StorageClasses().Create(context.Background(), &sc, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func deployPVC(tenantSetup TenantCluster) {
	pvc := v1.PersistentVolumeClaim{}
	pvcData := bytes.NewReader(generated.MustAsset("deploy/example/storage-claim.yaml"))
	err := yaml.NewYAMLToJSONDecoder(pvcData).Decode(&pvc)
	Expect(err).NotTo(HaveOccurred())
	_, err = tenantSetup.Client.CoreV1().PersistentVolumeClaims(tenantSetup.Namespace).Create(context.Background(), &pvc, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func deployTestPod(tenantSetup TenantCluster) error {
	testPod := v1.Pod{}
	podData := bytes.NewReader(generated.MustAsset("deploy/example/test-pod.yaml"))
	err := yaml.NewYAMLToJSONDecoder(podData).Decode(&testPod)
	Expect(err).NotTo(HaveOccurred())
	_, err = tenantSetup.Client.CoreV1().Pods(tenantSetup.Namespace).Create(context.Background(), &testPod, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	testPodUpTimeout, cancelFunc := context.WithTimeout(context.Background(), time.Minute*20)
	defer cancelFunc()
	_, err = clientwatch.UntilWithSync(
		testPodUpTimeout,
		cache.NewListWatchFromClient(tenantSetup.Client.CoreV1().RESTClient(), "pods",
			tenantSetup.Namespace, fields.OneTermEqualSelector("metadata.name", testPod.Name)),
		&v1.Pod{},
		nil,
		func(event apiwatch.Event) (bool, error) {
			switch event.Type {
			case apiwatch.Added, apiwatch.Modified:
				p, ok := event.Object.(*v1.Pod)
				if !ok {
					Fail("Failed fetching the test pod")
				}
				if p.Status.Phase == v1.PodRunning {
					return true, nil
				}
			default:
				return false, nil
			}

			return false, nil
		},
	)
	return nil
}

func destroyTestPod(tenantSetup TenantCluster) error {
	testPod := v1.Pod{}
	podData := bytes.NewReader(generated.MustAsset("deploy/example/test-pod.yaml"))
	err := yaml.NewYAMLToJSONDecoder(podData).Decode(&testPod)
	Expect(err).NotTo(HaveOccurred())
	return tenantSetup.Client.CoreV1().Pods(tenantSetup.Namespace).Delete(context.Background(), testPod.Name, metav1.DeleteOptions{})
}

func watchDVUnpluged(c *InfraCluster) (*apiwatch.Event, error) {
	testDVUnplugTimeout, cancelFunc := context.WithTimeout(context.Background(), time.Minute*10)
	defer cancelFunc()

	return clientwatch.UntilWithSync(
		testDVUnplugTimeout,
		cache.NewListWatchFromClient(c.VirtCli.RestClient(), "virtualmachineinstances",
			c.Namespace, fields.OneTermEqualSelector("metadata.name", K8sMachineName)),
		&kubevirtapiv1.VirtualMachineInstance{},
		nil,
		func(event apiwatch.Event) (bool, error) {
			switch event.Type {
			case apiwatch.Added, apiwatch.Modified:
				vm, ok := event.Object.(*kubevirtapiv1.VirtualMachineInstance)
				if !ok {
					Fail("couldn't detect a change in VMI " + K8sMachineName)
				}
				for _, vs := range vm.Status.VolumeStatus {
					if vs.HotplugVolume != nil && vs.HotplugVolume.AttachPodName != "" {
						// we have an attached volume - fail
						return false, nil
					}
				}
			default:
				return false, nil
			}

			// no more attached volumes.
			return true, nil
		},
	)
}

package common_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/apis/storage"
	"kubevirt.io/client-go/kubecli"

	"github.com/google/uuid"
	"github.com/kubevirt/csi-driver/pkg/generated"
	"github.com/onsi/ginkgo"

	ocproutev1 "github.com/openshift/api/route/v1"
	routesclientset "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/client-go/api/v1"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
	testscore "kubevirt.io/kubevirt/tests"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	apiwatch "k8s.io/apimachinery/pkg/watch"
	clientwatch "k8s.io/client-go/tools/watch"
)

const (
	TestStorageClass = "standard"
	K8sMachineName   = "k8s-machine"
	RouteName        = "k8s-api"
	TenantConfigName = "tenant-config"
)

var running = true

type InfraCluster struct {
	// kubevirt's client for creating needed resources to install the cluster on
	VirtCli    kubecli.KubevirtClient
	Kubeconfig clientcmd.ClientConfig
	// the infra Namespace for creating resources
	Namespace string
}

var testNamespace = "kubevirt-csi-driver-func-test-" + uuid.New().String()[0:7]
var teardownNamespace = true
func init() {
	if v, ok := os.LookupEnv("KUBEVIRT_CSI_DRIVER_FUNC_TEST_NAMESPACE"); ok {
		testNamespace = v
		teardownNamespace = false
	}

}

type TenantCluster struct {
	Namespace  string
	Config     *api.Config
	Client     *kubernetes.Clientset
	RestConfig *rest.Config
}

func PrepareEnv(clusterSetup InfraCluster) {
	clusterSetup.setupTenantCluster()
}

func TearDownEnv(infraCluster InfraCluster) {
	if teardownNamespace {
		err := infraCluster.VirtCli.CoreV1().
			Namespaces().Delete(context.Background(), infraCluster.Namespace, metav1.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred(), "Failed deleting the namespace post suite. Please clean manually")
	}
}

func (c *InfraCluster) setupTenantCluster() {
	fmt.Fprint(ginkgo.GinkgoWriter, "Preparing infrastructure for the tenant cluster\n")
	c.createNamespace()
	c.createAPIRoute()
	c.exposeTenantAPI()
	c.createServiceAccount()
	c.createVm()
}

func (c *InfraCluster) exposeTenantAPI() error {
	_, err := c.VirtCli.CoreV1().Services(c.Namespace).Create(context.Background(), &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api",
		},
		Spec: v1.ServiceSpec{
			Ports:           []v1.ServicePort{{Port: 6443, TargetPort: intstr.IntOrString{IntVal: 6443}, Protocol: v1.ProtocolTCP}},
			Selector:        map[string]string{"kubevirt.io/domain": K8sMachineName},
			Type:            "NodePort",
			SessionAffinity: "None",
		},
	}, metav1.CreateOptions{})
	return err
}

func (c *InfraCluster) createServiceAccount() error {
	fmt.Fprint(ginkgo.GinkgoWriter, "Creating service account...\n")
	reader := bytes.NewReader(generated.MustAsset("deploy/infra-cluster-service-account.yaml"))
	sa := v1.ServiceAccount{}
	err := yaml.NewYAMLToJSONDecoder(reader).Decode(&sa)
	//printErr(err)
	if err != nil {
		return nil
	}
	_, err = c.VirtCli.CoreV1().ServiceAccounts(c.Namespace).Create(context.Background(), &sa, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (c *InfraCluster) createAPIRoute() (*ocproutev1.Route, error) {
	fmt.Fprint(ginkgo.GinkgoWriter, "Creating API endpoint route...\n")
	r := ocproutev1.Route{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Route",
			APIVersion: ocproutev1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: RouteName,
		},
		Spec: ocproutev1.RouteSpec{
			TLS: &ocproutev1.TLSConfig{
				Termination: ocproutev1.TLSTerminationPassthrough},
			To: ocproutev1.RouteTargetReference{
				Kind: "Service",
				Name: "api",
			},
		},
	}

	routes, err := routesclientset.NewForConfig(c.VirtCli.Config())
	if err != nil {
		return nil, err
	}

	route, err := routes.Routes(c.Namespace).Create(context.Background(), &r, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		return nil, err
	}
	return route, nil
}

func (c *InfraCluster) createNamespace() {
	fmt.Fprint(ginkgo.GinkgoWriter, "Creating the test namespace...\n")
	_, err := c.VirtCli.CoreV1().Namespaces().Create(context.Background(), &v1.Namespace{
		ObjectMeta: k8smetav1.ObjectMeta{
			Name: c.Namespace,
		},
	}, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

func (c *InfraCluster) createVm() {
	routes, err := routesclientset.NewForConfig(c.VirtCli.Config())
	Expect(err).NotTo(HaveOccurred())
	route, err := routes.Routes(c.Namespace).Get(context.Background(), RouteName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	fmt.Fprint(ginkgo.GinkgoWriter, "Creating a VM for k8s deployment...\n")

	k8sMachine, rootDv, secret := newK8sMachine(c.Kubeconfig, *route, c.Namespace)

	_, err = c.VirtCli.CdiClient().CdiV1alpha1().DataVolumes(c.Namespace).Create(context.Background(), rootDv, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	_, err = c.VirtCli.CoreV1().Secrets(c.Namespace).Create(context.Background(), secret, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	_, err = c.VirtCli.VirtualMachine(c.Namespace).Create(k8sMachine)
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	Eventually(func() bool {
		vmi, _ := c.VirtCli.VirtualMachineInstance(c.Namespace).Get(K8sMachineName, &metav1.GetOptions{})
		return vmi.Status.Phase == kubevirtv1.Running
	}, 40*time.Minute, 30*time.Second).Should(BeTrue(), "failed to get the vmi Running")

}

func (c *InfraCluster) CreateObject(restConfig rest.Config, discovery discovery.DiscoveryInterface, namespace string, reader *bytes.Reader) (runtime.Object, error) {
	objs, err := objectsFromYAML(reader)
	if err != nil {
		return nil, err
	}
	var createdObjs []runtime.Object
	for _, obj := range objs {
		gvk := obj.GetObjectKind().GroupVersionKind()
		restClient, _ := newRestClient(restConfig, gvk.GroupVersion())
		groupResources, err := restmapper.GetAPIGroupResources(discovery)
		if err != nil {
			return nil, err
		}
		rm := restmapper.NewDiscoveryRESTMapper(groupResources)
		mapping, err := rm.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, err
		}
		restHelper := resource.NewHelper(restClient, mapping)

		created, err := restHelper.Create(namespace, true, obj)
		if err != nil && !errors.IsAlreadyExists(err) {
			return nil, err
		}
		createdObjs = append(createdObjs, created)
	}
	// for now support returning the first object only.
	return createdObjs[0], nil
}

func GetInfraCluster() InfraCluster {
	client, err := kubecli.GetKubevirtClient()
	Expect(err).NotTo(HaveOccurred(), "Failed getting KubeVirt client")

	kubeconfig := flag.Lookup("kubeconfig").Value
	Expect(kubeconfig.String()).NotTo(BeEmpty())
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig.String()},
		&clientcmd.ConfigOverrides{})

	infraCluster := InfraCluster{
		client,
		config,
		testNamespace,
	}
	return infraCluster
}

func newK8sMachine(config clientcmd.ClientConfig, route ocproutev1.Route, namespace string) (*kubevirtv1.VirtualMachine, *cdiv1.DataVolume, *corev1.Secret) {
	vmiTemplateSpec := &kubevirtv1.VirtualMachineInstanceTemplateSpec{}
	vm := kubevirtv1.VirtualMachine{
		ObjectMeta: k8smetav1.ObjectMeta{
			Name: K8sMachineName,
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			Running:  &running,
			Template: vmiTemplateSpec,
		},
	}

	mem := apiresource.MustParse("16G")
	vmiTemplateSpec.ObjectMeta.Labels = map[string]string{
		"kubevirt.io/domain": K8sMachineName,
	}
	vmiTemplateSpec.Spec.Domain = kubevirtv1.DomainSpec{
		CPU: &kubevirtv1.CPU{
			Cores:   4,
			Sockets: 1,
		},
		Memory: &kubevirtv1.Memory{
			Guest: &mem,
		},
	}
	vmiTemplateSpec.Spec.Domain.Resources.Requests = corev1.ResourceList{
		corev1.ResourceMemory: mem,
	}

	dataVolume := newDataVolume(
		"root-disk-dv",
		"https://cloud.centos.org/centos/7/images/CentOS-7-x86_64-GenericCloud-2009.qcow2c",
		"15G",
		TestStorageClass)

	cloudInitDisk := "cloudinit-disk"
	rootDisk := "root-disk"
	bus := "virtio"
	vmiTemplateSpec.Spec.Domain.Devices.Disks = append(vmiTemplateSpec.Spec.Domain.Devices.Disks, kubevirtv1.Disk{
		Name: cloudInitDisk,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{
				Bus: bus,
			},
		},
	})
	var first uint = 1
	vmiTemplateSpec.Spec.Domain.Devices.Disks = append(vmiTemplateSpec.Spec.Domain.Devices.Disks, kubevirtv1.Disk{
		BootOrder: &first,
		Name:      rootDisk,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{
				Bus: bus,
			},
		},
	})

	rawConfig, _ := config.RawConfig()
	kubeconfig, _ := clientcmd.Write(rawConfig)
	b64Config := base64.StdEncoding.EncodeToString(kubeconfig)
	cloudinitdata = strings.Replace(cloudinitdata, "@@infra-cluster-kubeconfig@@", b64Config, 1)
	cloudinitdata = strings.Replace(cloudinitdata, "@@tenant-cluster-name@@", route.Spec.Host, 1)
	cloudinitdata = strings.Replace(cloudinitdata, "@@test-namespace@@", namespace, 1)
	secret := corev1.Secret{
		ObjectMeta: k8smetav1.ObjectMeta{
			Name: "userdata",
		},
		Data: map[string][]byte{"userdata": []byte(cloudinitdata)},
	}
	vmiTemplateSpec.Spec.Volumes = append(vmiTemplateSpec.Spec.Volumes, kubevirtv1.Volume{
		Name: cloudInitDisk,
		VolumeSource: kubevirtv1.VolumeSource{
			CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
				UserDataSecretRef: &corev1.LocalObjectReference{Name: "userdata"},
			},
		},
	})

	vmiTemplateSpec.Spec.Volumes = append(vmiTemplateSpec.Spec.Volumes, kubevirtv1.Volume{
		Name: rootDisk,
		VolumeSource: kubevirtv1.VolumeSource{
			DataVolume: &kubevirtv1.DataVolumeSource{
				Name: dataVolume.Name,
			},
		},
	})

	return &vm, dataVolume, &secret
}


func objectsFromYAML(reader *bytes.Reader) ([]runtime.Object, error) {
	// register CSI v1. CSI v1 is missing because client-go is pinned to 0.16 because of kubevirt client deps.
	newScheme := runtime.NewScheme()
	newScheme.AddKnownTypes(
		schema.GroupVersion{Group: storage.GroupName, Version: "v1"},
		&storage.CSINode{},
		&storage.CSINodeList{},
		&storage.CSIDriver{},
		&storage.CSIDriverList{},
	)
	//factory := serializer.NewCodecFactory(newScheme)
	//csiDecode := factory.UniversalDecoder(schema.GroupVersion{Group: storage.GroupName, Version: "v1"}).Decode
	decode := scheme.Codecs.UniversalDeserializer().Decode
	all, _ := ioutil.ReadAll(reader)
	d := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(all), 4096)
	// data from reader may contain multi objects, stream and get a list
	var o []runtime.Object
	for {
		ext := runtime.RawExtension{}
		if err := d.Decode(&ext); err != nil {
			if err == io.EOF {
				break
			}
		}
		ext.Raw = bytes.TrimSpace(ext.Raw)
		if len(ext.Raw) == 0 || bytes.Equal(ext.Raw, []byte("null")) {
			continue
		}
		obj, _, err := decode(ext.Raw, nil, nil)
		if err != nil {
			return nil, err
		}
		o = append(o, obj)
	}
	return o, nil
}

func newRestClient(restConfig rest.Config, gv schema.GroupVersion) (rest.Interface, error) {
	restConfig.ContentConfig = resource.UnstructuredPlusDefaultContentConfig()
	restConfig.GroupVersion = &gv
	if len(gv.Group) == 0 {
		restConfig.APIPath = "/api"
	} else {
		restConfig.APIPath = "/apis"
	}

	return rest.RESTClientFor(&restConfig)
}

func newDataVolume(name, imageUrl, diskSize, storageClass string) *cdiv1.DataVolume {
	quantity, err := apiresource.ParseQuantity(diskSize)
	testscore.PanicOnError(err)
	dataVolume := &cdiv1.DataVolume{
		ObjectMeta: k8smetav1.ObjectMeta{
			Name: name,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				HTTP: &cdiv1.DataVolumeSourceHTTP{
					URL: imageUrl,
				},
			},
			PVC: &corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"storage": quantity,
					},
				},
				StorageClassName: &storageClass,
			},
		},
	}

	dataVolume.TypeMeta = k8smetav1.TypeMeta{
		APIVersion: "cdi.kubevirt.io/v1alpha1",
		Kind:       "DataVolume",
	}

	return dataVolume
}

func GetTenantCluster(c InfraCluster) TenantCluster {
	waitForTenant(c)
	// get the tenant kubeconfig
	tenantConfigMap, err := c.VirtCli.CoreV1().
		ConfigMaps(c.Namespace).Get(context.Background(), TenantConfigName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	routes, err := routesclientset.NewForConfig(c.VirtCli.Config())
	Expect(err).NotTo(HaveOccurred())
	route, err := routes.Routes(c.Namespace).Get(context.Background(), RouteName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())

	tenantCluster, err := getTenantClusterSetup(tenantConfigMap, route)
	Expect(err).NotTo(HaveOccurred())
	return *tenantCluster
}

func waitForTenant(c InfraCluster) {
	fmt.Fprintf(GinkgoWriter, "Wait for tenant to be up... ")
	// wait till the config map is set by the tenant. This means the install is successful
	// and we can extract the kubeconfig
	tenantUpTimeout, cancelFunc := context.WithTimeout(context.Background(), time.Minute*40)
	defer cancelFunc()
	_, _ = clientwatch.UntilWithSync(
		tenantUpTimeout,
		cache.NewListWatchFromClient(c.VirtCli.CoreV1().RESTClient(), "configmaps",
			c.Namespace, fields.OneTermEqualSelector("metadata.name", TenantConfigName)),
		&v1.ConfigMap{},
		nil,
		func(event apiwatch.Event) (bool, error) {
			switch event.Type {
			case apiwatch.Added:
			default:
				return false, nil
			}
			_, ok := event.Object.(*v1.ConfigMap)
			if !ok {
				Fail("couldn't find config map 'tenant-config' in the namespace. Tenant cluster might failed installation")
			}

			fmt.Fprintf(GinkgoWriter, "up and running\n")
			return true, nil
		},
	)
}

func getTenantClusterSetup(cm *v1.ConfigMap, route *ocproutev1.Route) (*TenantCluster, error) {
	// substitute the internal API IP with the ingress route
	tenantConfigData := cm.Data["admin.conf"]
	tenantConfig, err := clientcmd.Load([]byte(tenantConfigData))
	tenantConfig.Clusters["kubernetes"].Server = fmt.Sprintf("https://%s", route.Spec.Host)
	if err != nil {
		return nil, err
	}
	clientcmd.WriteToFile(*tenantConfig, "/home/isaakdorfman/.kube/tmp")
	if err != nil {
		return nil, err
	}
	config, err := clientcmd.NewDefaultClientConfig(*tenantConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	tenantClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	version, err := tenantClient.ServerVersion()
	fmt.Fprintf(GinkgoWriter, "Tenant server version %v\n", version)
	Expect(err).NotTo(HaveOccurred())

	return &TenantCluster{
		Namespace:  "kubevirt-csi-driver",
		Config:     tenantConfig,
		RestConfig: config,
		Client:     tenantClient,
	}, nil
}

func DeploySecretWithInfraDetails(c InfraCluster, tenantSetup TenantCluster) {
	s := v1.Secret{}
	secretData := bytes.NewReader(generated.MustAsset("deploy/secret.yaml"))
	err := yaml.NewYAMLToJSONDecoder(secretData).Decode(&s)
	Expect(err).NotTo(HaveOccurred())

	s.Data = make(map[string][]byte)
	infraKubeconfig, err := c.Kubeconfig.RawConfig()
	Expect(err).NotTo(HaveOccurred())
	kubeconfigData, err := clientcmd.Write(infraKubeconfig)
	Expect(err).NotTo(HaveOccurred())
	s.Data["kubeconfig"] = kubeconfigData
	_, err = tenantSetup.Client.CoreV1().Secrets(tenantSetup.Namespace).Create(context.Background(), &s, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}

	cm := v1.ConfigMap{}
	cmData := bytes.NewReader(generated.MustAsset("deploy/configmap.yaml"))
	err = yaml.NewYAMLToJSONDecoder(cmData).Decode(&cm)
	Expect(err).NotTo(HaveOccurred())
	cm.Data["infraClusterNamespace"] = c.Namespace
	_, err = tenantSetup.Client.CoreV1().ConfigMaps(tenantSetup.Namespace).Create(context.Background(), &cm, metav1.CreateOptions{})
	if !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

var cloudinitdata = `#cloud-config
password: centos
chpasswd:
  expire: false

ssh_authorized_keys:
  - ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDSlI9o9rPYrlVvXTtCt02A+Gcf2674ZPBFLpkaNzjsiM4bTTNNOJ6PgnOs6dBWxT2XPOSQO6y13CeWGdZLuLXhczz8Y4KB520tkPW3fKjDiJqneWEd1sxPzdHy1Vf4icxJIXMcx7Mia/8B2XrXNsTbQnOCjRJT0dEbIbXSELLnTEYfc+L3z2+klNKos2JNchfuiTw2FLGrY5x9CwX9Nanrx6kGDPmVO68ugtsQL20mKMwuGCnEkIPGsNP0eN0Bk1vVR+k0MDaII5nUpK1glh2reL3BPVCFhKh2xzASvQqK2mr8gqzbhA/LSVA8awNnib5o55beP7vr/yECkpe0f931LD3hg1O8qEeubfxKbOcUY1rDUOnyOuetB2bNE8TiAnrtY/xne8mhhBnobHwUkUMVU3J6szm58AaoQmclVsogBIetF0bi75CnOD9fY84SLsCKbHLCGkfEXXEnjErAD27UDGMSD3EmCefP41VBcq/nR7E/ns4aqLn8GIPOMt4amPM= rgolan@rgolan.local
output: {all: '| tee -a /var/log/cloud-init-output.log'}
write_files:
  - content: |
      net.bridge.bridge-nf-call-ip6tables = 1
      net.bridge.bridge-nf-call-iptables = 1
    path: /etc/sysctl.d/k8s.conf
  - content: |
      [kubernetes]
      name=Kubernetes
      baseurl=https://packages.cloud.google.com/yum/repos/kubernetes-el7-\$basearch
      enabled=1
      gpgcheck=1
      repo_gpgcheck=1
      gpgkey=https://packages.cloud.google.com/yum/doc/yum-key.gpg https://packages.cloud.google.com/yum/doc/rpm-package-key.gpg
      exclude=kubelet kubeadm kubectl
    path: /etc/yum.repos.d/kubernetes.repo
  - content: @@infra-cluster-kubeconfig@@
    encoding: base64
    path: /root/infracluster.kubeconfig

runcmd:
  - sysctl --system
  - yum-config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo
  - yum install -y docker kubeadm
  - systemctl enable --now docker
  - setenforce 0
  - sed -i 's/^SELINUX=enforcing$/SELINUX=permissive/' /etc/selinux/config
  - yum install -y kubelet kubeadm kubectl --disableexcludes=kubernetes
  - systemctl enable --now kubelet
  - kubeadm init --pod-network-cidr=192.168.0.0/16 --apiserver-cert-extra-sans @@tenant-cluster-name@@
  - while true; do kubectl --kubeconfig=/etc/kubernetes/admin.conf version && break || sleep 5; done
  - kubectl --kubeconfig=/etc/kubernetes/admin.conf apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml
  - kubectl --kubeconfig=/etc/kubernetes/admin.conf taint nodes --all node-role.kubernetes.io/master-
  - kubectl --kubeconfig=/root/infracluster.kubeconfig create cm tenant-config -n @@test-namespace@@ --from-file=/etc/kubernetes/admin.conf
  - mkdir -p /root/.kube
  - cp -i /etc/kubernetes/admin.conf /root/.kube/config
  - chown $(id -u):$(id -g) /root/.kube/config
`

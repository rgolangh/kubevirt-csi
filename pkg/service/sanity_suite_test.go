package service

import (
	"fmt"
	"io/ioutil"

	"github.com/kubernetes-csi/csi-test/pkg/sanity"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("MyCSIDriver", func() {
	Context("Config A", func() {
		config = sanity.Config{}
		testDir = ioutil.TempDir("/tmp/kubevirt-csi-driver-tests", "sanity")

		config.Address = testDir + "csi.sock"
		//config.ControllerAddress , "controllerendpoint", "CSI controller endpoint")
		config.TargetPath = testDir + "mountdir"
		config.StagingPath = testDir + "stagingdir"
		// config.CreateTargetPathCmd, "createmountpathcmd", "Command to run for target path creation")
		// config.CreateStagingPathCmd, "createstagingpathcmd", "Command to run for staging path creation")
		// config.CreatePathCmdTimeout, "createpathcmdtimeout", "Timeout for the commands to create target and staging paths, in seconds")
		// config.RemoveTargetPathCmd, "removemountpathcmd", "Command to run for target path removal")
		// config.RemoveStagingPathCmd, "removestagingpathcmd", "Command to run for staging path removal")
		// config.RemovePathCmdTimeout, "removepathcmdtimeout", "Timeout for the commands to remove target and staging paths, in seconds")
		// config.SecretsFile, "secrets", "CSI secrets file")
		// config.TestVolumeAccessType, "testvolumeaccesstype", "Volume capability access type, valid values are mount or block")
		// config.TestVolumeSize, "testvolumesize", "Base volume size used for provisioned volumes")
		// config.TestVolumeExpandSize, "testvolumeexpandsize", "Target size for expanded volumes")
		// config.TestVolumeParametersFile, "testvolumeparameters", "YAML file of volume parameters for provisioned volumes")
		// config.TestSnapshotParametersFile, "testsnapshotparameters", "YAML file of snapshot parameters for provisioned snapshots")
		// config.TestNodeVolumeAttachLimit, "testnodevolumeattachlimit", "Test node volume attach limit")

		BeforeSuite(func() {
			fmt.Println("before suite")
		})

		BeforeEach(func() {
			//... setup driver and config...
		})

		AfterEach(func() {
			//...tear down driver...
		})

		Describe("CSI sanity", func() {
			sanity.GinkgoTest(config)
		})
	})

	Context("Config B", func() {
		// other configs
	})
})

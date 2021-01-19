package machinescope

import (
	"fmt"
	"testing"
	"time"

	kubevirtproviderv1alpha1 "github.com/openshift/cluster-api-provider-kubevirt/pkg/apis/kubevirtprovider/v1alpha1"
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/testutils"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	"gotest.tools/assert"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtapiv1 "kubevirt.io/client-go/api/v1"
)

func TestUpdateAllowed(t *testing.T) {
	requeueAfterSeconds := 20

	cases := []struct {
		name           string
		expectedResult bool
		modifyMachine  func(machine *machinev1.Machine)
	}{
		{
			name:           "allowed LastUpdated empty",
			expectedResult: true,
		},
		{
			name:           "allowed LastUpdated not empty",
			expectedResult: true,
			modifyMachine: func(machine *machinev1.Machine) {
				now := time.Now()
				duration := time.Duration(-1*(requeueAfterSeconds-1)) * time.Second
				lastUpdated := now.Add(duration)

				machine.Status.LastUpdated = &metav1.Time{
					Time: lastUpdated,
				}
			},
		},
		{
			name:           "not allowed ProviderID nil",
			expectedResult: false,
			modifyMachine: func(machine *machinev1.Machine) {
				machine.Spec.ProviderID = nil
			},
		},
		{
			name:           "not allowed ProviderID empty",
			expectedResult: false,
			modifyMachine: func(machine *machinev1.Machine) {
				emptyProviderID := ""
				machine.Spec.ProviderID = &emptyProviderID
			},
		},
		{
			name:           "not allowed time passed since LastUpdated too short",
			expectedResult: false,
			modifyMachine: func(machine *machinev1.Machine) {
				now := time.Now()
				duration := time.Duration(-1*requeueAfterSeconds) * time.Second
				lastUpdated := now.Add(duration)

				machine.Status.LastUpdated = &metav1.Time{
					Time: lastUpdated,
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machine, err := testutils.StubMachine()
			if err != nil {
				t.Fatalf("Error durring stubMachine creation: %v", err)
			}
			if tc.modifyMachine != nil {
				tc.modifyMachine(machine)
			}
			machineScope, err := New().CreateMachineScope(machine, "testInfraNamespace", "testInfraID")
			if err != nil {
				t.Fatalf("Error durring machineScope creation: %v", err)
			}
			result := machineScope.UpdateAllowed(time.Duration(requeueAfterSeconds))
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestCreateIgnitionSecretFromMachine(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	expectedResult := testutils.StubIgnitionSecret()
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.CreateIgnitionSecretFromMachine([]byte(fmt.Sprintf(testutils.FullUserDataFmt, testutils.MachineName)))
	assert.DeepEqual(t, expectedResult, result)
}

func TestSyncMachine(t *testing.T) {
	cases := []struct {
		name        string
		expectedErr string
		modify      func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine)
	}{
		{
			name: "success status created and ready",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) {
				vm.Status.Created = true
				vm.Status.Ready = true
				machine.Annotations["machine.openshift.io/instance-state"] = "vmWasCreatedAndReady"
			},
		},
		{
			name: "success status created and not ready",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) {
				vm.Status.Created = true
				vm.Status.Ready = false
				machine.Annotations["machine.openshift.io/instance-state"] = "vmWasCreatedButNotReady"
			},
		},
		{
			name: "success status not Created",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) {
				vm.Status.Created = false
				vm.Status.Ready = false
				machine.Annotations["machine.openshift.io/instance-state"] = "vmNotCreated"
			},
		},
		{
			name: "success status created and ready",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) {
				vm.Status.Created = true
				vm.Status.Ready = true
				machine.Annotations["machine.openshift.io/instance-state"] = "vmWasCreatedAndReady"
				vm.Spec.Template = nil
				delete(machine.Labels, "machine.openshift.io/instance-type")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machine, err := testutils.StubMachine()
			if err != nil {
				t.Fatalf("Error durring stubMachine creation: %v", err)
			}
			expectedResultMachine, err := testutils.StubMachine()
			if err != nil {
				t.Fatalf("Error durring stubMachine creation: %v", err)
			}
			machineScope, err := New().CreateMachineScope(machine, "testInfraNamespace", "testInfraID")
			if err != nil {
				t.Fatalf("Error durring machineScope creation: %v", err)
			}

			vmName := "test-vm-name"
			vmNamespace := "test-vm-namespace"
			vmID := "test-vm-id"
			machineType := "test-machine-type"

			vm := kubevirtapiv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      vmName,
					Namespace: vmNamespace,
					UID:       types.UID(vmID),
				},
			}
			vmi := testutils.StubVirtualMachineInstance()

			providerID := fmt.Sprintf("kubevirt://%s/%s", vmNamespace, vmName)
			expectedResultMachine.Spec.ProviderID = &providerID
			expectedResultMachine.Annotations = map[string]string{"VmId": vmID}
			if tc.modify != nil {
				tc.modify(expectedResultMachine, &vm)
			}
			vm.Spec.Template = &kubevirtapiv1.VirtualMachineInstanceTemplateSpec{}
			vm.Spec.Template.Spec.Domain.Machine.Type = machineType
			expectedResultMachine.Labels["machine.openshift.io/instance-type"] = machineType
			providerStatus, err := kubevirtproviderv1alpha1.RawExtensionFromProviderStatus(&kubevirtproviderv1alpha1.KubevirtMachineProviderStatus{
				VirtualMachineStatus: vm.Status,
			})
			if err != nil {
				t.Fatalf("Error durring providerStatus creation: %v", err)
			}
			expectedResultMachine.Status.ProviderStatus = providerStatus
			expectedResultMachine.Status.Addresses = []corev1.NodeAddress{
				{Address: vmi.Name, Type: corev1.NodeInternalDNS},
				{Type: corev1.NodeInternalIP, Address: "127.0.0.1"},
			}

			err = machineScope.SyncMachine(vm, *testutils.StubVirtualMachineInstance())
			if tc.expectedErr != "" {
				assert.Error(t, err, tc.expectedErr)
			} else {
				assert.NilError(t, err)
				assert.DeepEqual(t, machine, expectedResultMachine)
			}
		})
	}
}

func TestCreateVirtualMachineFromMachine(t *testing.T) {
	cases := []struct {
		name        string
		modify      func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error
		expectedErr string
	}{
		{
			name: "success",
		},
		{
			name: "success default accessMode",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.PersistentVolumeAccessMode = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}
				vm.Spec.DataVolumeTemplates[0].Spec.PVC.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}

				return err
			},
		},
		{
			name: "success default storageClass",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.StorageClassName = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}
				vm.Spec.DataVolumeTemplates[0].Spec.PVC.StorageClassName = nil

				return err
			},
		},
		{
			name: "success default storage size",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.RequestedStorage = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}
				vm.Spec.DataVolumeTemplates[0].Spec.PVC.Resources.Requests[corev1.ResourceStorage] = apiresource.MustParse("35Gi")

				return err
			},
		},
		{
			name: "success default memory",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.RequestedMemory = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}
				vm.Spec.Template.Spec.Domain.Resources.Requests[corev1.ResourceMemory] = apiresource.MustParse("2048M")

				return err
			},
		},
		{
			name: "failure source pvc name empty",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.SourcePvcName = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}

				return err
			},
			expectedErr: "test-machine-name: missing value for SourcePvcName",
		},
		{
			name: "failure ignition secret name empty",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.IgnitionSecretName = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}

				return err
			},
			expectedErr: "test-machine-name: missing value for IgnitionSecretName",
		},
		{
			name: "failure network name empty",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.NetworkName = ""
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}

				return err
			},
			expectedErr: "test-machine-name: missing value for NetworkName",
		},
		{
			name: "failure access mode not valid",
			modify: func(machine *machinev1.Machine, vm *kubevirtapiv1.VirtualMachine) error {
				modifyProviderSpec := testutils.ProviderSpec
				modifyProviderSpec.PersistentVolumeAccessMode = "NotValid"
				val, err := kubevirtproviderv1alpha1.RawExtensionFromProviderSpec(&modifyProviderSpec)
				machine.Spec.ProviderSpec = machinev1.ProviderSpec{Value: val}

				return err
			},
			expectedErr: "test-machine-name: Value of PersistentVolumeAccessMode, can be only one of: ReadWriteMany, ReadOnlyMany, ReadWriteOnce",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machine, err := testutils.StubMachine()
			if err != nil {
				t.Fatalf("Error durring stubMachine creation: %v", err)
			}
			expectedVM := testutils.StubVirtualMachine()
			if tc.modify != nil {
				if err := tc.modify(machine, expectedVM); err != nil {
					t.Fatalf("Error durring update machine and virtual machine: %v", err)
				}
			}

			machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
			if err != nil {
				t.Fatalf("Error durring machineScope creation: %v", err)
			}
			result, err := machineScope.CreateVirtualMachineFromMachine()
			if tc.expectedErr != "" {
				assert.Error(t, err, tc.expectedErr)
			} else {
				assert.NilError(t, err)
				assert.DeepEqual(t, result, expectedVM)
			}
		})
	}
}

func TestGetMachine(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.GetMachine()
	assert.Equal(t, machine, result)
}

func TestGetMachineName(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.GetMachineName()
	assert.Equal(t, machine.GetName(), result)
}

func TestGetMachineNamespace(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.GetMachineNamespace()
	assert.Equal(t, machine.GetNamespace(), result)
}

func TestGetInfraNamespace(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.GetInfraNamespace()
	assert.Equal(t, testutils.InfraNamespace, result)
}

func TestGetIgnitionSecretName(t *testing.T) {
	machine, err := testutils.StubMachine()
	if err != nil {
		t.Fatalf("Error durring stubMachine creation: %v", err)
	}
	machineScope, err := New().CreateMachineScope(machine, testutils.InfraNamespace, testutils.InfraID)
	if err != nil {
		t.Fatalf("Error durring machineScope creation: %v", err)
	}
	result := machineScope.GetIgnitionSecretName()
	assert.Equal(t, testutils.IgnitionSecretName, result)
}

package kubevirt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openshift/cluster-api-provider-kubevirt/pkg/clients/infracluster"
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/machinescope"
	"k8s.io/apimachinery/pkg/api/errors"

	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
	kubevirtapiv1 "kubevirt.io/client-go/api/v1"
)

const (
	requeueAfterSeconds      = 20
	requeueAfterFatalSeconds = 180
	masterLabel              = "node-role.kubevirt.io/master"
)

//go:generate mockgen -source=./kubevirt.go -destination=./mock/kubevirt_generated.go -package=mock
// KubevirtVM runs the logic to reconciles a machine resource towards its desired state
type KubevirtVM interface {
	Create(machineScope machinescope.MachineScope, userData []byte) error
	Delete(machineScope machinescope.MachineScope) error
	Update(machineScope machinescope.MachineScope) (bool, error)
	Exists(machineScope machinescope.MachineScope) (bool, error)
}

// manager is the struct which implement KubevirtVM interface
// Use infraClusterClientBuilder to create the infra cluster vms
type manager struct {
	infraClusterClient infracluster.Client
}

// New creates provider vm instance
func New(infraClusterClient infracluster.Client) KubevirtVM {
	return &manager{
		infraClusterClient: infraClusterClient,
	}
}

// Create creates machine if it does not exists.
func (m *manager) Create(machineScope machinescope.MachineScope, userData []byte) (resultErr error) {
	machineName := machineScope.GetMachineName()

	fullUserData, err := addHostnameToUserData(userData, machineName)
	if err != nil {
		return err
	}

	secretFromMachine := machineScope.CreateIgnitionSecretFromMachine(fullUserData)

	if _, err := m.infraClusterClient.CreateSecret(context.Background(), secretFromMachine.Namespace, secretFromMachine); err != nil {
		msg := fmt.Sprintf("%s: Error during Create: failed to create ignition secret in infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	virtualMachineFromMachine, err := machineScope.CreateVirtualMachineFromMachine()
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Create: failed to build Virtual Machine struct, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	createdVM, err := m.infraClusterClient.CreateVirtualMachine(context.Background(), virtualMachineFromMachine.Namespace, virtualMachineFromMachine)
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Create: failed to create Virtual Machine in infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	klog.Infof("%s: VirtualMachine was created in infracluster for the Machine", machineName)

	return m.syncMachine(*createdVM, machineScope, machineName, "Create")
}

func addHostnameToUserData(src []byte, hostname string) ([]byte, error) {
	var dataMap map[string]interface{}
	json.Unmarshal([]byte(src), &dataMap)
	if _, ok := dataMap["storage"]; !ok {
		dataMap["storage"] = map[string]interface{}{}
	}
	storage := (dataMap["storage"]).(map[string]interface{})
	if _, ok := storage["files"]; !ok {
		storage["files"] = []map[string]interface{}{}
	}
	newFile := map[string]interface{}{
		"filesystem": "root",
		"path":       "/etc/hostname",
		"mode":       420,
	}
	newFile["contents"] = map[string]interface{}{
		"source": fmt.Sprintf("data:,%s", hostname),
	}
	storage["files"] = append(storage["files"].([]map[string]interface{}), newFile)
	result, err := json.Marshal(dataMap)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// delete deletes machine
func (m *manager) Delete(machineScope machinescope.MachineScope) error {
	machineName := machineScope.GetMachineName()

	virtualMachineFromMachine, err := machineScope.CreateVirtualMachineFromMachine()
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Delete: failed to build Virtual Machine struct, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	existingVM, err := m.getInraClusterVM(virtualMachineFromMachine.GetName(), virtualMachineFromMachine.GetNamespace(), machineScope)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("%s: Virtual Machine does not exist (already deleted - return)", machineName)
			return nil
		}

		msg := fmt.Sprintf("%s: Error during Delete: failed to get Virtual Machine from infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	gracePeriod := int64(10)
	if err := m.infraClusterClient.DeleteVirtualMachine(context.Background(),
		existingVM.GetNamespace(),
		existingVM.GetName(),
		&k8smetav1.DeleteOptions{GracePeriodSeconds: &gracePeriod}); err != nil {
		msg := fmt.Sprintf("%s: Error during Delete: failed to delete Virtual Machine in infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}

	klog.Infof("%s: VirtualMachine was deleted in infracluster for the Machine", machineName)

	return nil
}

// update finds a vm and reconciles the machine resource status against it.
func (m *manager) Update(machineScope machinescope.MachineScope) (bool, error) {
	machineName := machineScope.GetMachineName()

	virtualMachineFromMachine, err := machineScope.CreateVirtualMachineFromMachine()
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Update: failed to build Virtual Machine struct, with error: %v", machineName, err)
		klog.Errorf(msg)
		return false, fmt.Errorf(msg)
	}

	existingVM, err := m.getInraClusterVM(virtualMachineFromMachine.GetName(), virtualMachineFromMachine.GetNamespace(), machineScope)
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Update: failed to get Virtual Machine from infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return false, fmt.Errorf(msg)
	}

	previousResourceVersion := existingVM.ResourceVersion
	virtualMachineFromMachine.ObjectMeta.ResourceVersion = previousResourceVersion

	//TODO remove it after pushing that PR: https://github.com/kubevirt/kubevirt/pull/3889
	virtualMachineFromMachine.Status = kubevirtapiv1.VirtualMachineStatus{
		Created: existingVM.Status.Created,
		Ready:   existingVM.Status.Ready,
	}

	updatedVM, err := m.infraClusterClient.UpdateVirtualMachine(context.Background(), virtualMachineFromMachine.Namespace, virtualMachineFromMachine)
	if err != nil {
		msg := fmt.Sprintf("%s: Error during Update: failed to update Virtual Machine in infraCluster, with error: %v", machineName, err)
		klog.Errorf(msg)
		return false, fmt.Errorf(msg)
	}
	currentResourceVersion := updatedVM.ResourceVersion

	klog.Infof("%s: VirtualMachine was updated in infracluster for the Machine", machineName)

	wasUpdated := previousResourceVersion != currentResourceVersion
	err = m.syncMachine(*updatedVM, machineScope, machineName, "Update")

	return wasUpdated, err
}

func (m *manager) syncMachine(vm kubevirtapiv1.VirtualMachine, machineScope machinescope.MachineScope, machineName string, operation string) error {
	vmi, err := m.infraClusterClient.GetVirtualMachineInstance(context.Background(), vm.Namespace, vm.Name, &k8smetav1.GetOptions{})
	if err != nil {
		msg := fmt.Sprintf("%s: Error during %s: failed to get vmi of the Machine, with error: %v", machineName, operation, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}
	if err := machineScope.SyncMachine(vm, *vmi); err != nil {
		msg := fmt.Sprintf("%s: Error during %s: failed to sync the Machine, with error: %v", machineName, operation, err)
		klog.Errorf(msg)
		return fmt.Errorf(msg)
	}
	return nil
}

// exists returns true if machine exists.
func (m *manager) Exists(machineScope machinescope.MachineScope) (bool, error) {
	machineName := machineScope.GetMachineName()
	infraNamespace := machineScope.GetInfraNamespace()

	klog.Infof("%s: check if machine exists", machineName)
	_, err := m.getInraClusterVM(machineName, infraNamespace, machineScope)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("%s: Virtual Machine of this Machine does not exist", machineName)
			return false, nil
		}
		msg := fmt.Sprintf("%s: Error during Exists: failed to get vm of the Machine, with error: %v", machineName, err)
		klog.Errorf(msg)
		return false, fmt.Errorf(msg)
	}

	return true, nil
}

func (m *manager) getInraClusterVM(vmName, vmNamespace string, machineScope machinescope.MachineScope) (*kubevirtapiv1.VirtualMachine, error) {
	return m.infraClusterClient.GetVirtualMachine(context.Background(), vmNamespace, vmName, &k8smetav1.GetOptions{})
}

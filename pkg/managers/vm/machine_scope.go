package vm

import (
	"fmt"
	"time"

	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"

	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"

	kubevirtproviderv1alpha1 "github.com/kubevirt/cluster-api-provider-kubevirt/pkg/apis/kubevirtprovider/v1alpha1"
	"github.com/kubevirt/cluster-api-provider-kubevirt/pkg/clients/overkube"
	"github.com/kubevirt/cluster-api-provider-kubevirt/pkg/clients/underkube"
	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/klog"
	kubevirtapiv1 "kubevirt.io/client-go/api/v1"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
)

type machineState string

const (
	vmNotCreated      machineState = "vmNotCreated"
	vmCreatedNotReady machineState = "vmWasCreatedButNotReady"
	vmCreatedAndReady machineState = "vmWasCreatedButAndReady"
)

const (
	pvcRequestsStorage                = "35Gi"
	defaultRequestedMemory            = "2048M"
	defaultPersistentVolumeAccessMode = corev1.ReadWriteOnce
	defaultDataVolumeDiskName         = "datavolumedisk1"
	defaultCloudInitVolumeDiskName    = "cloudinitdisk"
	defaultBootVolumeDiskName         = "bootvolume"
	kubevirtIdAnnotationKey           = "VmId"
	userDataKey                       = "userData"
	defaultBus                        = "virtio"
)

type machineScope struct {
	underkubeClient       underkube.Client
	overkubeClient        overkube.Client
	machine               *machinev1.Machine
	originMachineCopy     *machinev1.Machine
	machineProviderSpec   *kubevirtproviderv1alpha1.KubevirtMachineProviderSpec
	machineProviderStatus *kubevirtproviderv1alpha1.KubevirtMachineProviderStatus
}

func newMachineScope(machine *machinev1.Machine, overkubeClient overkube.Client, underkubeClientBuilder underkube.ClientBuilderFuncType) (*machineScope, error) {
	if err := validateMachine(*machine); err != nil {
		return nil, fmt.Errorf("%v: failed validating machine provider spec: %w", machine.GetName(), err)
	}

	providerSpec, err := kubevirtproviderv1alpha1.ProviderSpecFromRawExtension(machine.Spec.ProviderSpec.Value)
	if err != nil {
		return nil, machinecontroller.InvalidMachineConfiguration("failed to get machine config: %v", err)
	}

	providerStatus, err := kubevirtproviderv1alpha1.ProviderStatusFromRawExtension(machine.Status.ProviderStatus)
	if err != nil {
		return nil, machinecontroller.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}

	kubevirtClient, err := underkubeClientBuilder(overkubeClient, providerSpec.SecretName, machine.GetNamespace())
	if err != nil {
		return nil, machinecontroller.InvalidMachineConfiguration("failed to create aKubeVirt client: %v", err.Error())
	}

	return &machineScope{
		underkubeClient:       kubevirtClient,
		overkubeClient:        overkubeClient,
		machine:               machine,
		originMachineCopy:     machine.DeepCopy(),
		machineProviderSpec:   providerSpec,
		machineProviderStatus: providerStatus,
	}, nil
}

func getVMNamespace(machine *machinev1.Machine) string {
	return machine.Namespace
}

func (s *machineScope) machineToVirtualMachine() (*kubevirtapiv1.VirtualMachine, error) {
	runAlways := kubevirtapiv1.RunStrategyAlways
	// use getClusterID as a namespace
	// TODO: if there isnt a cluster id - need to return an error
	namespace := getVMNamespace(s.machine)

	vmiTemplate, err := s.buildVMITemplate(namespace)
	if err != nil {
		return nil, err
	}

	virtualMachine := kubevirtapiv1.VirtualMachine{
		Spec: kubevirtapiv1.VirtualMachineSpec{
			RunStrategy: &runAlways,
			DataVolumeTemplates: []cdiv1.DataVolume{
				*buildBootVolumeDataVolumeTemplate(s.machine.GetName(), s.machineProviderSpec.SourcePvcName, namespace, s.machineProviderSpec.SourcePvcNamespace),
			},
			Template: vmiTemplate,
		},
		Status: s.machineProviderStatus.VirtualMachineStatus,
	}

	virtualMachine.TypeMeta = s.machine.TypeMeta
	virtualMachine.ObjectMeta = metav1.ObjectMeta{
		Name:            s.machine.Name,
		Namespace:       namespace,
		Labels:          s.machine.Labels,
		Annotations:     s.machine.Annotations,
		OwnerReferences: s.machine.OwnerReferences,
		ClusterName:     s.machine.ClusterName,
		ResourceVersion: s.machineProviderStatus.ResourceVersion,
	}

	return &virtualMachine, nil
}

func (s *machineScope) getMachineName() string {
	return s.machine.GetName()
}

func (s *machineScope) getMachineNamespace() string {
	return s.machine.GetNamespace()
}

// setProviderID adds providerID in the machine spec
func (s *machineScope) setProviderID(vm *kubevirtapiv1.VirtualMachine) {
	// TODO: return an error when the setting is failed
	existingProviderID := s.machine.Spec.ProviderID
	if vm == nil {
		return
	}

	providerID := fmt.Sprintf("kubevirt:///%s/%s", s.getMachineNamespace(), vm.GetName())

	if existingProviderID != nil && *existingProviderID == providerID {
		klog.Infof("%s: ProviderID already set in the machine Spec with value:%s", s.getMachineName(), *existingProviderID)
		return
	}

	s.machine.Spec.ProviderID = &providerID
	klog.Infof("%s: ProviderID set at machine spec: %s", s.getMachineName(), providerID)
}

// updateAllowed validates that updates come in the right order
// if there is an update that was supposes to be done after that update - return an error
func (s *machineScope) updateAllowed() bool {
	return s.machine.Spec.ProviderID != nil && *s.machine.Spec.ProviderID != "" && (s.machine.Status.LastUpdated == nil || s.machine.Status.LastUpdated.Add(requeueAfterSeconds*time.Second).After(time.Now()))
}

func buildDataVolumeDiskName(virtualMachineName string) string {
	return buildVolumeName(virtualMachineName, defaultDataVolumeDiskName)
}
func buildCloudInitVolumeDiskName(virtualMachineName string) string {
	return buildVolumeName(virtualMachineName, defaultCloudInitVolumeDiskName)
}

func buildBootVolumeName(virtualMachineName string) string {
	return buildVolumeName(virtualMachineName, defaultBootVolumeDiskName)
}

func buildVolumeName(virtualMachineName, suffixVolumeName string) string {
	return fmt.Sprintf("%s-%s", virtualMachineName, suffixVolumeName)
}

func (s *machineScope) buildVMITemplate(namespace string) (*kubevirtapiv1.VirtualMachineInstanceTemplateSpec, error) {
	virtualMachineName := s.machine.GetName()

	template := &kubevirtapiv1.VirtualMachineInstanceTemplateSpec{}

	template.ObjectMeta = metav1.ObjectMeta{
		Labels: map[string]string{"kubevirt.io/vm": virtualMachineName},
	}

	userData, err := s.getUserData(namespace)
	if err != nil {
		return nil, err
	}

	template.Spec = kubevirtapiv1.VirtualMachineInstanceSpec{}
	template.Spec.Volumes = []kubevirtapiv1.Volume{
		{
			Name: buildDataVolumeDiskName(virtualMachineName),
			VolumeSource: kubevirtapiv1.VolumeSource{
				DataVolume: &kubevirtapiv1.DataVolumeSource{
					Name: buildBootVolumeName(virtualMachineName),
				},
			},
		},
		{
			Name: buildCloudInitVolumeDiskName(virtualMachineName),
			VolumeSource: kubevirtapiv1.VolumeSource{
				CloudInitConfigDrive: &kubevirtapiv1.CloudInitConfigDriveSource{
					UserData: userData,
				},
			},
		},
	}

	template.Spec.Domain = kubevirtapiv1.DomainSpec{}

	requests := corev1.ResourceList{}

	requestedMemory := s.machineProviderSpec.RequestedMemory
	if requestedMemory == "" {
		requestedMemory = defaultRequestedMemory
	}
	requests[corev1.ResourceMemory] = apiresource.MustParse(requestedMemory)

	if s.machineProviderSpec.RequestedCPU != 0 {
		requests[corev1.ResourceCPU] = apiresource.MustParse(fmt.Sprint(s.machineProviderSpec.RequestedCPU))
	}

	template.Spec.Domain.Resources = kubevirtapiv1.ResourceRequirements{
		Requests: requests,
	}
	template.Spec.Domain.Machine = kubevirtapiv1.Machine{Type: s.machineProviderSpec.MachineType}
	template.Spec.Domain.Devices = kubevirtapiv1.Devices{
		Disks: []kubevirtapiv1.Disk{
			{
				Name: buildDataVolumeDiskName(virtualMachineName),
				DiskDevice: kubevirtapiv1.DiskDevice{
					Disk: &kubevirtapiv1.DiskTarget{
						Bus: defaultBus,
					},
				},
			},
			{
				Name: buildCloudInitVolumeDiskName(virtualMachineName),
				DiskDevice: kubevirtapiv1.DiskDevice{
					Disk: &kubevirtapiv1.DiskTarget{
						Bus: defaultBus,
					},
				},
			},
		},
	}

	return template, nil
}

func (s *machineScope) getUserData(namespace string) (string, error) {
	secretName := s.machineProviderSpec.IgnitionSecretName
	userDataSecret, err := s.overkubeClient.UserDataSecret(secretName, s.machine.GetNamespace())
	if err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return "", machinecontroller.InvalidMachineConfiguration("Overkube credentials secret %s/%s: %v not found", namespace, secretName, err)
		}
		return "", err
	}
	userDataByte, ok := userDataSecret.Data[userDataKey]
	if !ok {
		return "", machinecontroller.InvalidMachineConfiguration("Overkube credentials secret %s/%s: %v doesn't contain the key", namespace, secretName, userDataKey)
	}
	userData := string(userDataByte)
	return userData, nil
}

func buildBootVolumeDataVolumeTemplate(virtualMachineName, pvcName, dvNamespace, pvcNamespace string) *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		TypeMeta: metav1.TypeMeta{APIVersion: cdiv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildBootVolumeName(virtualMachineName),
			Namespace: dvNamespace,
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: cdiv1.DataVolumeSource{
				PVC: &cdiv1.DataVolumeSourcePVC{
					Name:      pvcName,
					Namespace: dvNamespace,
					//Namespace: pvcNamespace,
				},
			},
			PVC: &corev1.PersistentVolumeClaimSpec{
				// TODO: Need to determine it by the type of storage class: pvc.Spec.StorageClassName
				AccessModes: []corev1.PersistentVolumeAccessMode{
					defaultPersistentVolumeAccessMode,
				},
				// TODO: Where to get it?? - add as a list
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: apiresource.MustParse(pvcRequestsStorage),
					},
				},
			},
		},
	}
}

func (s *machineScope) SyncMachineFromVm(vm *kubevirtapiv1.VirtualMachine, vmi *kubevirtapiv1.VirtualMachineInstance) error {
	s.setProviderID(vm)

	if err := s.setMachineAnnotationsAndLabels(vm); err != nil {
		return fmt.Errorf("failed to set machine cloud provider specifics: %w", err)
	}

	if err := s.setProviderStatus(vm, vmi, conditionSuccess()); err != nil {
		return machinecontroller.InvalidMachineConfiguration("failed to set machine provider status: %v", err.Error())
	}

	klog.Infof("Updated machine %s", s.getMachineName())
	return nil
}

func (s *machineScope) setMachineAnnotationsAndLabels(vm *kubevirtapiv1.VirtualMachine) error {
	if vm == nil {
		return nil
	}

	if s.machine.Labels == nil {
		s.machine.Labels = make(map[string]string)
	}

	if s.machine.Spec.Labels == nil {
		s.machine.Spec.Labels = make(map[string]string)
	}

	if s.machine.Annotations == nil {
		s.machine.Annotations = make(map[string]string)
	}
	vmId := vm.UID
	vmType := vm.Spec.Template.Spec.Domain.Machine.Type

	vmState := vmNotCreated
	if vm.Status.Created {
		vmState = vmCreatedNotReady
		if vm.Status.Ready {
			vmState = vmCreatedAndReady
		}
	}

	s.machine.ObjectMeta.Annotations[kubevirtIdAnnotationKey] = string(vmId)
	s.machine.Labels[machinecontroller.MachineInstanceTypeLabelName] = vmType
	s.machine.Annotations[machinecontroller.MachineInstanceStateAnnotationName] = string(vmState)

	return nil
}

// Patch patches the machine spec and machine status after reconciling.
func (s *machineScope) patchMachine() error {

	klog.V(3).Infof("%v: patching machine", s.machine.GetName())

	providerStatus, err := kubevirtproviderv1alpha1.RawExtensionFromProviderStatus(s.machineProviderStatus)
	if err != nil {
		return machinecontroller.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}
	s.machine.Status.ProviderStatus = providerStatus

	// patch machine
	statusCopy := *s.machine.Status.DeepCopy()
	if err := s.overkubeClient.PatchMachine(s.machine, s.originMachineCopy); err != nil {
		klog.Errorf("Failed to patch machine %q: %v", s.machine.GetName(), err)
		return err
	}

	s.machine.Status = statusCopy

	// patch status
	if err := s.overkubeClient.StatusPatchMachine(s.machine, s.originMachineCopy); err != nil {
		klog.Errorf("Failed to patch machine status %q: %v", s.machine.GetName(), err)
		return err
	}

	return nil
}

func machineProviderStatusFromVirtualMachine(virtualMachine *kubevirtapiv1.VirtualMachine) *kubevirtproviderv1alpha1.KubevirtMachineProviderStatus {
	return &kubevirtproviderv1alpha1.KubevirtMachineProviderStatus{
		VirtualMachineStatus: virtualMachine.Status,
		ResourceVersion:      virtualMachine.ResourceVersion,
	}
}

func (s *machineScope) setProviderStatus(vm *kubevirtapiv1.VirtualMachine, vmi *kubevirtapiv1.VirtualMachineInstance, condition kubevirtapiv1.VirtualMachineCondition) error {
	if vm == nil {
		klog.Infof("%s: couldn't calculate KubeVirt status - the provided vm is empty", s.machine.GetName())
		return nil
	}
	klog.Infof("%s: Updating status", s.machine.GetName())
	var networkAddresses []corev1.NodeAddress
	s.machineProviderStatus = machineProviderStatusFromVirtualMachine(vm)

	// update nodeAddresses
	networkAddresses = append(networkAddresses, corev1.NodeAddress{Address: vm.Name, Type: corev1.NodeInternalDNS})

	// VMI might be nil while the vm is in creating state but the vmi wasn't created yet.
	//For example when colning the VM's dv
	if vmi != nil {
		// Copy specific addresses - only node addresses.
		addresses, err := extractNodeAddresses(vmi)
		if err != nil {
			klog.Errorf("%s: Error extracting vm IP addresses: %v", s.machine.GetName(), err)
			return err
		}
		networkAddresses = append(networkAddresses, addresses...)
	}

	klog.Infof("%s: finished calculating KubeVirt status", s.machine.GetName())

	s.machine.Status.Addresses = networkAddresses
	// TODO: update the phase of the machine
	//s.machine.Status.Phase = setKubevirtMachineProviderCondition(condition, vm.Status.Conditions)

	return nil
}

// GetMachineName return the name of the provided Machine
func GetMachineName(machine *machinev1.Machine) string {
	return machine.GetName()
}

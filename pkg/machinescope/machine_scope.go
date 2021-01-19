package machinescope

import (
	"fmt"
	"net"
	"time"

	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"

	kubevirtproviderv1alpha1 "github.com/openshift/cluster-api-provider-kubevirt/pkg/apis/kubevirtprovider/v1alpha1"
	providerctrl "github.com/openshift/cluster-api-provider-kubevirt/pkg/providerid"
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/utils"
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
	vmCreatedAndReady machineState = "vmWasCreatedAndReady"
)

const (
	defaultRequestedMemory            = "2048M"
	defaultRequestedStorage           = "35Gi"
	defaultPersistentVolumeAccessMode = corev1.ReadWriteMany
	defaultDataVolumeDiskName         = "datavolumedisk1"
	defaultCloudInitVolumeDiskName    = "cloudinitdisk"
	defaultBootVolumeDiskName         = "bootvolume"
	kubevirtIdAnnotationKey           = "VmId"
	defaultBus                        = "virtio"
	APIVersion                        = "kubevirt.io/v1alpha3"
	Kind                              = "VirtualMachine"
	mainNetworkName                   = "main"
	terminationGracePeriodSeconds     = 600
)

type MachineScopeCreator interface {
	CreateMachineScope(machine *machinev1.Machine, infraNamespace string, infraID string) (MachineScope, error)
}

type machineScopeCreator struct{}

func New() MachineScopeCreator {
	return machineScopeCreator{}
}

//go:generate mockgen -source=./machine_scope.go -destination=./mock/machine_scope_generated.go -package=mock
type MachineScope interface {
	UpdateAllowed(requeueAfterSeconds time.Duration) bool
	CreateIgnitionSecretFromMachine(userData []byte) *corev1.Secret
	SyncMachine(vm kubevirtapiv1.VirtualMachine, vmi kubevirtapiv1.VirtualMachineInstance) error
	CreateVirtualMachineFromMachine() (*kubevirtapiv1.VirtualMachine, error)
	GetMachine() *machinev1.Machine
	GetMachineName() string
	GetMachineNamespace() string
	GetInfraNamespace() string
	GetIgnitionSecretName() string
}

type machineScope struct {
	machine             *machinev1.Machine
	machineProviderSpec *kubevirtproviderv1alpha1.KubevirtMachineProviderSpec
	infraNamespace      string
	infraID             string
}

func (creator machineScopeCreator) CreateMachineScope(machine *machinev1.Machine, infraNamespace string, infraID string) (MachineScope, error) {
	// TODO: insert a validation on machine labels
	if machine.Labels[machinev1.MachineClusterIDLabel] == "" {
		return nil, machinecontroller.InvalidMachineConfiguration("%v: missing %q label", machine.GetName(), machinev1.MachineClusterIDLabel)
	}

	providerSpec, err := kubevirtproviderv1alpha1.ProviderSpecFromRawExtension(machine.Spec.ProviderSpec.Value)
	if err != nil {
		return nil, machinecontroller.InvalidMachineConfiguration("failed to get machine config: %v", err)
	}

	if err != nil {
		return nil, machinecontroller.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}

	return &machineScope{
		machine:             machine,
		machineProviderSpec: providerSpec,
		infraNamespace:      infraNamespace,
		infraID:             infraID,
	}, nil
}

func (s *machineScope) GetInfraNamespace() string {
	return s.infraNamespace
}

func (s *machineScope) CreateVirtualMachineFromMachine() (*kubevirtapiv1.VirtualMachine, error) {
	if err := s.assertMandatoryParams(); err != nil {
		return nil, err
	}
	runAlways := kubevirtapiv1.RunStrategyAlways

	vmiTemplate := s.buildVMITemplate(s.infraNamespace)

	pvcRequestsStorage := s.machineProviderSpec.RequestedStorage
	if pvcRequestsStorage == "" {
		pvcRequestsStorage = defaultRequestedStorage
	}
	PVCAccessMode := defaultPersistentVolumeAccessMode
	if s.machineProviderSpec.PersistentVolumeAccessMode != "" {
		accessMode := corev1.PersistentVolumeAccessMode(s.machineProviderSpec.PersistentVolumeAccessMode)
		switch accessMode {
		case corev1.ReadWriteMany:
			PVCAccessMode = corev1.ReadWriteMany
		case corev1.ReadOnlyMany:
			PVCAccessMode = corev1.ReadOnlyMany
		case corev1.ReadWriteOnce:
			PVCAccessMode = corev1.ReadWriteOnce
		default:
			return nil, machinecontroller.InvalidMachineConfiguration("%v: Value of PersistentVolumeAccessMode, can be only one of: %v, %v, %v",
				s.machine.GetName(), corev1.ReadWriteMany, corev1.ReadOnlyMany, corev1.ReadWriteOnce)
		}
	}

	virtualMachine := kubevirtapiv1.VirtualMachine{
		Spec: kubevirtapiv1.VirtualMachineSpec{
			RunStrategy: &runAlways,
			DataVolumeTemplates: []cdiv1.DataVolume{
				*buildBootVolumeDataVolumeTemplate(
					s.machine.GetName(),
					s.machineProviderSpec.SourcePvcName,
					s.infraNamespace,
					s.machineProviderSpec.StorageClassName,
					pvcRequestsStorage,
					PVCAccessMode,
				),
			},
			Template: vmiTemplate,
		},
	}

	labels := utils.BuildLabels(s.infraID)
	for k, v := range s.machine.Labels {
		labels[k] = v
	}

	virtualMachine.APIVersion = APIVersion
	virtualMachine.Kind = Kind
	virtualMachine.ObjectMeta = metav1.ObjectMeta{
		Name:            s.machine.Name,
		Namespace:       s.infraNamespace,
		Labels:          labels,
		Annotations:     s.machine.Annotations,
		OwnerReferences: nil,
		ClusterName:     s.machine.ClusterName,
	}

	return &virtualMachine, nil
}

func (s *machineScope) assertMandatoryParams() error {
	switch {
	case s.machineProviderSpec.SourcePvcName == "":
		return machinecontroller.InvalidMachineConfiguration("%v: missing value for SourcePvcName", s.machine.GetName())
	case s.machineProviderSpec.IgnitionSecretName == "":
		return machinecontroller.InvalidMachineConfiguration("%v: missing value for IgnitionSecretName", s.machine.GetName())
	case s.machineProviderSpec.NetworkName == "":
		return machinecontroller.InvalidMachineConfiguration("%v: missing value for NetworkName", s.machine.GetName())
	default:
		return nil
	}
}

func (s *machineScope) buildVMITemplate(namespace string) *kubevirtapiv1.VirtualMachineInstanceTemplateSpec {
	virtualMachineName := s.machine.GetName()

	template := &kubevirtapiv1.VirtualMachineInstanceTemplateSpec{}

	template.ObjectMeta = metav1.ObjectMeta{
		Labels: map[string]string{"kubevirt.io/vm": virtualMachineName, "name": virtualMachineName},
	}

	ignitionSecretName := buildIgnitionSecretName(virtualMachineName)

	terminationGracePeriod := int64(terminationGracePeriodSeconds)
	template.Spec = kubevirtapiv1.VirtualMachineInstanceSpec{
		TerminationGracePeriodSeconds: &terminationGracePeriod,
	}
	template.Spec.Volumes = []kubevirtapiv1.Volume{
		{
			Name: defaultDataVolumeDiskName,
			VolumeSource: kubevirtapiv1.VolumeSource{
				DataVolume: &kubevirtapiv1.DataVolumeSource{
					Name: buildBootVolumeName(virtualMachineName),
				},
			},
		},
		{
			Name: defaultCloudInitVolumeDiskName,
			VolumeSource: kubevirtapiv1.VolumeSource{
				CloudInitConfigDrive: &kubevirtapiv1.CloudInitConfigDriveSource{
					UserDataSecretRef: &corev1.LocalObjectReference{
						Name: ignitionSecretName,
					},
				},
			},
		},
	}
	multusNetwork := &kubevirtapiv1.MultusNetwork{
		NetworkName: s.machineProviderSpec.NetworkName,
	}
	template.Spec.Networks = []kubevirtapiv1.Network{
		{
			Name: mainNetworkName,
			NetworkSource: kubevirtapiv1.NetworkSource{
				Multus: multusNetwork,
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
	template.Spec.Domain.Devices = kubevirtapiv1.Devices{
		Disks: []kubevirtapiv1.Disk{
			{
				Name: defaultDataVolumeDiskName,
				DiskDevice: kubevirtapiv1.DiskDevice{
					Disk: &kubevirtapiv1.DiskTarget{
						Bus: defaultBus,
					},
				},
			},
			{
				Name: defaultCloudInitVolumeDiskName,
				DiskDevice: kubevirtapiv1.DiskDevice{
					Disk: &kubevirtapiv1.DiskTarget{
						Bus: defaultBus,
					},
				},
			},
		},
		Interfaces: []kubevirtapiv1.Interface{
			{
				Name: mainNetworkName,
				InterfaceBindingMethod: kubevirtapiv1.InterfaceBindingMethod{
					Bridge: &kubevirtapiv1.InterfaceBridge{},
				},
			},
		},
	}

	return template
}

func (s *machineScope) GetMachine() *machinev1.Machine {
	return s.machine
}

func (s *machineScope) GetMachineName() string {
	return s.machine.GetName()
}

func (s *machineScope) GetMachineNamespace() string {
	return s.machine.GetNamespace()
}

// updateAllowed validates that updates come in the right order
// if there is an update that was supposes to be done after that update - return an error
func (s *machineScope) UpdateAllowed(requeueAfterSeconds time.Duration) bool {
	return s.machine.Spec.ProviderID != nil &&
		*s.machine.Spec.ProviderID != "" &&
		(s.machine.Status.LastUpdated == nil ||
			s.machine.Status.LastUpdated.Add(requeueAfterSeconds*time.Second).After(time.Now()))
}

func buildBootVolumeName(virtualMachineName string) string {
	return fmt.Sprintf("%s-%s", virtualMachineName, defaultBootVolumeDiskName)
}

func buildIgnitionSecretName(virtualMachineName string) string {
	return fmt.Sprintf("%s-ignition", virtualMachineName)
}

func (s *machineScope) CreateIgnitionSecretFromMachine(userData []byte) *corev1.Secret {
	virtualMachineName := s.machine.GetName()
	ignitionSecretName := buildIgnitionSecretName(virtualMachineName)
	labels := utils.BuildLabels(s.infraID)

	resultSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ignitionSecretName,
			Namespace: s.infraNamespace,
			Labels:    labels,
		},
		Data: map[string][]byte{
			"userdata": userData,
		},
	}

	return resultSecret
}

func (s *machineScope) GetIgnitionSecretName() string {
	return s.machineProviderSpec.IgnitionSecretName
}

func buildBootVolumeDataVolumeTemplate(virtualMachineName, pvcName, dvNamespace, storageClassName,
	pvcRequestsStorage string, accessMode corev1.PersistentVolumeAccessMode) *cdiv1.DataVolume {

	persistentVolumeClaimSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{
			accessMode,
		},
		// TODO: Where to get it?? - add as a list
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: apiresource.MustParse(pvcRequestsStorage),
			},
		},
	}
	if storageClassName != "" {
		persistentVolumeClaimSpec.StorageClassName = &storageClassName
	}

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
				},
			},
			PVC: &persistentVolumeClaimSpec,
		},
	}
}

func (s *machineScope) SyncMachine(vm kubevirtapiv1.VirtualMachine, vmi kubevirtapiv1.VirtualMachineInstance) error {
	s.syncProviderID(vm)
	s.syncMachineAnnotationsAndLabels(vm)
	s.syncNetworkAddresses(vmi)
	return s.syncProviderStatus(vm)
}

// syncProviderID adds providerID in the machine spec
func (s *machineScope) syncProviderID(vm kubevirtapiv1.VirtualMachine) {
	existingProviderID := s.machine.Spec.ProviderID

	providerID := providerctrl.FormatProviderID(vm.GetNamespace(), vm.GetName())

	if existingProviderID != nil && *existingProviderID == providerID {
		klog.Infof("%s - syncProviderID: already synced with providerID %s", s.GetMachineName(), *existingProviderID)
		return
	}

	s.machine.Spec.ProviderID = &providerID
	klog.Infof("%s - syncProviderID: successfully synced machine.Spec.ProviderID to %s", s.GetMachineName(), providerID)
}

func (s *machineScope) syncMachineAnnotationsAndLabels(vm kubevirtapiv1.VirtualMachine) {
	if s.machine.Labels == nil {
		s.machine.Labels = make(map[string]string)
	}

	if s.machine.Annotations == nil {
		s.machine.Annotations = make(map[string]string)
	}

	vmState := vmNotCreated
	if vm.Status.Created {
		vmState = vmCreatedNotReady
		if vm.Status.Ready {
			vmState = vmCreatedAndReady
		}
	}

	s.machine.ObjectMeta.Annotations[kubevirtIdAnnotationKey] = string(vm.UID)
	if vm.Spec.Template != nil {
		s.machine.Labels[machinecontroller.MachineInstanceTypeLabelName] = vm.Spec.Template.Spec.Domain.Machine.Type
	}
	s.machine.Annotations[machinecontroller.MachineInstanceStateAnnotationName] = string(vmState)
	klog.Infof("%s - syncMachineAnnotationsAndLabels: successfully synced", s.GetMachineName())
}

func (s *machineScope) syncProviderStatus(vm kubevirtapiv1.VirtualMachine) error {
	providerStatus, err := kubevirtproviderv1alpha1.RawExtensionFromProviderStatus(&kubevirtproviderv1alpha1.KubevirtMachineProviderStatus{
		VirtualMachineStatus: vm.Status,
	})
	if err != nil {
		return machinecontroller.InvalidMachineConfiguration("failed to get machine provider status: %v", err.Error())
	}
	s.machine.Status.ProviderStatus = providerStatus
	klog.Infof("%s - syncProviderStatus: successfully synced machine.Status.ProviderStatus to %s", s.GetMachineName(), providerStatus)
	return nil
}

func (s *machineScope) syncNetworkAddresses(vmi kubevirtapiv1.VirtualMachineInstance) {
	// update nodeAddresses
	networkAddresses := []corev1.NodeAddress{{Address: vmi.Name, Type: corev1.NodeInternalDNS}}
	if ips, err := net.LookupIP(vmi.Name); err == nil {
		for _, ip := range ips {
			if ip.To4() != nil {
				networkAddresses = append(networkAddresses, corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip.String()})
			}
		}
	}

	s.machine.Status.Addresses = networkAddresses
	klog.Infof("%s - syncNetworkAddresses: successfully synced machine.Status.Addresses to %s", s.GetMachineName(), networkAddresses)
}

// TODO: update the phase of the machine
//s.machine.Status.Phase = setKubevirtMachineProviderCondition(condition, vm.Status.Conditions)
// func (s *machineScope) conditionSuccess() kubevirtapiv1.VirtualMachineCondition {
// 	return kubevirtapiv1.VirtualMachineCondition{
// 		Type:    kubevirtapiv1.VirtualMachineFailure,
// 		Status:  corev1.ConditionFalse,
// 		Reason:  "MachineCreationSucceeded",
// 		Message: "Machine successfully created",
// 	}
// }

/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package actuator

import (
	"context"
	"fmt"

	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"

	"github.com/openshift/cluster-api-provider-kubevirt/pkg/clients/tenantcluster"
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/kubevirt"
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/machinescope"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	scopeFailFmt      = "%s: failed to create scope for machine: %w"
	vmsFailFmt        = "%s: kubevirt wrapper failed to %s machine: %w"
	createEventAction = "Create"
	updateEventAction = "Update"
	deleteEventAction = "Delete"
	noEventAction     = ""
	userDataKey       = "userData"
)

const (
	ConfigMapNamespace             = "openshift-config"
	ConfigMapName                  = "cloud-provider-config"
	ConfigMapDataKeyName           = "config"
	ConfigMapInfraNamespaceKeyName = "namespace"
	ConfigMapInfraIDKeyName        = "infraID"
)

// Actuator is responsible for performing machine reconciliation.
type Actuator struct {
	eventRecorder       record.EventRecorder
	kubevirtVM          kubevirt.KubevirtVM
	machineScopeCreator machinescope.MachineScopeCreator
	tenantClusterClient tenantcluster.Client
	infraID             string
	infraNamespace      string
}

// New returns an actuator.
func New(kubevirtVM kubevirt.KubevirtVM,
	eventRecorder record.EventRecorder,
	machineScopeCreator machinescope.MachineScopeCreator,
	tenantClusterClient tenantcluster.Client) (*Actuator, error) {

	cMap, err := tenantClusterClient.GetConfigMapValue(context.Background(), ConfigMapName, ConfigMapNamespace, ConfigMapDataKeyName)
	if err != nil {
		return nil, nil
	}
	infraID, ok := (*cMap)[ConfigMapInfraIDKeyName]
	if !ok {
		return nil, machinecontroller.InvalidMachineConfiguration("Actuator: configMap %s/%s: The map extracted with key %s doesn't contain key %s",
			ConfigMapNamespace, ConfigMapName, ConfigMapDataKeyName, ConfigMapInfraIDKeyName)
	}
	infraNamespace, ok := (*cMap)[ConfigMapInfraNamespaceKeyName]
	if !ok {
		return nil, machinecontroller.InvalidMachineConfiguration("Actuator: configMap %s/%s: The map extracted with key %s doesn't contain key %s",
			ConfigMapNamespace, ConfigMapName, ConfigMapDataKeyName, ConfigMapInfraNamespaceKeyName)
	}
	return &Actuator{
		kubevirtVM:          kubevirtVM,
		eventRecorder:       eventRecorder,
		machineScopeCreator: machineScopeCreator,
		tenantClusterClient: tenantClusterClient,
		infraID:             infraID,
		infraNamespace:      infraNamespace,
	}, nil
}

func (a *Actuator) createMachineScope(machine *machinev1.Machine) (machinescope.MachineScope, error) {
	return a.machineScopeCreator.CreateMachineScope(machine, a.infraNamespace, a.infraID)
}

// Set corresponding event based on error. It also returns the original error
// for convenience, so callers can do "return handleMachineError(...)".
func (a *Actuator) handleMachineError(machine *machinev1.Machine, err error, eventAction string) error {
	machineScope, err := a.createMachineScope(machine)
	if err != nil {
		return err
	}

	klog.Errorf("%v error: %v", machineScope.GetMachineName(), err)
	if eventAction != noEventAction {
		a.eventRecorder.Eventf(machine, corev1.EventTypeWarning, "Failed"+eventAction, "%v", err)
	}
	return err
}

// Create creates a machine and is invoked by the machine controller.
func (a *Actuator) Create(ctx context.Context, machine *machinev1.Machine) error {
	originMachineCopy := machine.DeepCopy()
	machineScope, err := a.createMachineScope(machine)
	if err != nil {
		return err
	}

	klog.Infof("%s: actuator creating machine", machineScope.GetMachineName())

	userData, err := a.getUserData(machineScope)
	if err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, machineScope.GetMachineName(), createEventAction, err)
		return a.handleMachineError(machine, fmtErr, createEventAction)
	}
	err = a.kubevirtVM.Create(machineScope, userData)
	patchErr := a.patchMachine(machineScope.GetMachine(), originMachineCopy)
	if patchErr != nil {
		err = patchErr
	}
	if err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, machineScope.GetMachineName(), createEventAction, err)
		return a.handleMachineError(machine, fmtErr, createEventAction)
	}

	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, createEventAction, "Created Machine %v", machineScope.GetMachineName())
	return nil
}

func (a *Actuator) getUserData(machineScope machinescope.MachineScope) ([]byte, error) {
	secretName := machineScope.GetIgnitionSecretName()
	machineNamespace := machineScope.GetMachineNamespace()
	userDataSecret, err := a.tenantClusterClient.GetSecret(context.Background(), secretName, machineNamespace)
	if err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return nil, machinecontroller.InvalidMachineConfiguration("Tenant-cluster credentials secret %s/%s: %v not found", machineNamespace, secretName, err)
		}
		return nil, err
	}
	userData, ok := userDataSecret.Data[userDataKey]
	if !ok {
		return nil, machinecontroller.InvalidMachineConfiguration("Tenant-cluster credentials secret %s/%s: %v doesn't contain the key", machineNamespace, secretName, userDataKey)
	}
	return userData, nil
}

// Exists determines if the given machine currently exists.
// A machine which is not terminated is considered as existing.
func (a *Actuator) Exists(ctx context.Context, machine *machinev1.Machine) (bool, error) {
	machineScope, err := a.createMachineScope(machine)
	if err != nil {
		return false, err
	}

	klog.Infof("%s: actuator checking if machine exists", machineScope.GetMachineName())

	return a.kubevirtVM.Exists(machineScope)
}

// Update attempts to sync machine state with an existing instance.
func (a *Actuator) Update(ctx context.Context, machine *machinev1.Machine) error {
	originMachineCopy := machine.DeepCopy()
	machineScope, err := a.createMachineScope(machine)
	if err != nil {
		return err
	}

	klog.Infof("%s: actuator updating machine", machineScope.GetMachineName())

	wasUpdated, err := a.kubevirtVM.Update(machineScope)
	patchErr := a.patchMachine(machineScope.GetMachine(), originMachineCopy)
	if patchErr != nil {
		err = patchErr
	}
	if err != nil {

		fmtErr := fmt.Errorf(vmsFailFmt, machineScope.GetMachineName(), updateEventAction, err)
		return a.handleMachineError(machine, fmtErr, updateEventAction)
	}

	// Create event only if machine object was modified
	if wasUpdated {
		a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, updateEventAction, "Updated Machine %v", machineScope.GetMachineName())
	}

	return nil
}

// Delete deletes a machine and updates its finalizer
func (a *Actuator) Delete(ctx context.Context, machine *machinev1.Machine) error {
	machineScope, err := a.createMachineScope(machine)
	if err != nil {
		return err
	}

	klog.Infof("%s: actuator deleting machine", machineScope.GetMachineName())

	if err := a.kubevirtVM.Delete(machineScope); err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, machineScope.GetMachineName(), deleteEventAction, err)
		return a.handleMachineError(machine, fmtErr, deleteEventAction)
	}

	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, deleteEventAction, "Deleted machine %v", machineScope.GetMachineName())
	return nil
}

// Patch patches the machine spec and machine status after reconciling.
func (a *Actuator) patchMachine(machine *machinev1.Machine, originMachineCopy *machinev1.Machine) error {

	klog.V(3).Infof("%v: patching machine", machine.GetName())

	// patch machine
	statusCopy := *machine.Status.DeepCopy()
	if err := a.tenantClusterClient.PatchMachine(machine, originMachineCopy); err != nil {
		klog.Errorf("Failed to patch machine %q: %v", machine.GetName(), err)
		return err
	}

	machine.Status = statusCopy

	// patch status
	if err := a.tenantClusterClient.StatusPatchMachine(machine, originMachineCopy); err != nil {
		klog.Errorf("Failed to patch machine status %q: %v", machine.GetName(), err)
		return err
	}

	return nil
}

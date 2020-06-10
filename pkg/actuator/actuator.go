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

	"github.com/kubevirt/cluster-api-provider-kubevirt/pkg/managers/vm"
)

const (
	scopeFailFmt      = "%s: failed to create scope for machine: %w"
	vmsFailFmt        = "%s: kubevirt wrapper failed to %s machine: %w"
	createEventAction = "Create"
	updateEventAction = "Update"
	deleteEventAction = "Delete"
	noEventAction     = ""
)

// Actuator is responsible for performing machine reconciliation.
type Actuator struct {
	eventRecorder record.EventRecorder
	providerVM    vm.ProviderVM
}

// New returns an actuator.
func New(providerVM vm.ProviderVM, eventRecorder record.EventRecorder) *Actuator {
	return &Actuator{
		providerVM:    providerVM,
		eventRecorder: eventRecorder,
	}
}

// Set corresponding event based on error. It also returns the original error
// for convenience, so callers can do "return handleMachineError(...)".
func (a *Actuator) handleMachineError(machine *machinev1.Machine, err error, eventAction string) error {
	klog.Errorf("%v error: %v", vm.GetMachineName(machine), err)
	if eventAction != noEventAction {
		a.eventRecorder.Eventf(machine, corev1.EventTypeWarning, "Failed"+eventAction, "%v", err)
	}
	return err
}

// Create creates a machine and is invoked by the machine controller.
func (a *Actuator) Create(ctx context.Context, machine *machinev1.Machine) error {
	klog.Infof("%s: actuator creating machine", vm.GetMachineName(machine))

	if err := a.providerVM.Create(machine); err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, vm.GetMachineName(machine), createEventAction, err)
		return a.handleMachineError(machine, fmtErr, createEventAction)
	}

	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, createEventAction, "Created Machine %v", vm.GetMachineName(machine))
	return nil
}

// Exists determines if the given machine currently exists.
// A machine which is not terminated is considered as existing.
func (a *Actuator) Exists(ctx context.Context, machine *machinev1.Machine) (bool, error) {
	klog.Infof("%s: actuator checking if machine exists", vm.GetMachineName(machine))

	return a.providerVM.Exists(machine)
}

// Update attempts to sync machine state with an existing instance.
func (a *Actuator) Update(ctx context.Context, machine *machinev1.Machine) error {
	klog.Infof("%s: actuator updating machine", vm.GetMachineName(machine))

	previousResourceVersion := vm.GetMachineResourceVersion(machine)
	if err := a.providerVM.Update(machine); err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, vm.GetMachineName(machine), updateEventAction, err)
		return a.handleMachineError(machine, fmtErr, updateEventAction)
	}

	currentResourceVersion := vm.GetMachineResourceVersion(machine)

	// Create event only if machine object was modified
	if previousResourceVersion != currentResourceVersion {
		a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, updateEventAction, "Updated Machine %v", vm.GetMachineName(machine))
	}

	return nil
}

// Delete deletes a machine and updates its finalizer
func (a *Actuator) Delete(ctx context.Context, machine *machinev1.Machine) error {
	klog.Infof("%s: actuator deleting machine", vm.GetMachineName(machine))

	if err := a.providerVM.Delete(machine); err != nil {
		fmtErr := fmt.Errorf(vmsFailFmt, vm.GetMachineName(machine), deleteEventAction, err)
		return a.handleMachineError(machine, fmtErr, deleteEventAction)
	}

	a.eventRecorder.Eventf(machine, corev1.EventTypeNormal, deleteEventAction, "Deleted machine %v", vm.GetMachineName(machine))
	return nil
}

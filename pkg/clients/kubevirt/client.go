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

package kubevirt

import (
	kubernetesclient "github.com/kubevirt/cluster-api-provider-kubevirt/pkg/clients/kubernetes"
	machineapiapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/kubecli"
)

//go:generate mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

const (
	// UnderKubeConfig is secret key containing kubeconfig content of the UnderKube
	UnderKubeConfig = "kubeconfig"
)

// ClientBuilderFuncType is function type for building aws client
type ClientBuilderFuncType func(kubernetesClient kubernetesclient.Client, secretName, namespace string) (Client, error)

// Client is a wrapper object for actual kubevirt clients: virtctl and the kubecli
type Client interface {
	CreateVirtualMachine(namespace string, newVM *v1.VirtualMachine) (*v1.VirtualMachine, error)
	DeleteVirtualMachine(namespace string, name string, options *k8smetav1.DeleteOptions) error
	GetVirtualMachine(namespace string, name string, options *k8smetav1.GetOptions) (*v1.VirtualMachine, error)
	ListVirtualMachine(namespace string, options *k8smetav1.ListOptions) (*v1.VirtualMachineList, error)
	UpdateVirtualMachine(namespace string, vm *v1.VirtualMachine) (*v1.VirtualMachine, error)
	PatchVirtualMachine(namespace string, name string, pt types.PatchType, data []byte, subresources ...string) (result *v1.VirtualMachine, err error)
	RestartVirtualMachine(namespace string, name string) error
	StartVirtualMachine(namespace string, name string) error
	StopVirtualMachine(namespace string, name string) error
}

type kubevirtClient struct {
	kubecliclient kubecli.KubevirtClient
	//TODO: create a virtctl client also
	virtctlclient string
}

// New creates our client wrapper object for the actual KubeVirt and VirtCtl clients we use.
func New(kubernetesClient kubernetesclient.Client, secretName, namespace string) (Client, error) {
	if secretName == "" {
		return nil, machineapiapierrors.InvalidMachineConfiguration("KubeVirt credentials secret - Invalid empty secretName")
	}

	if namespace == "" {
		return nil, machineapiapierrors.InvalidMachineConfiguration("KubeVirt credentials secret - Invalid empty namespace")
	}

	userDataSecret, getSecretErr := kubernetesClient.UserDataSecret(secretName, namespace)
	if getSecretErr != nil {
		if apimachineryerrors.IsNotFound(getSecretErr) {
			return nil, machineapiapierrors.InvalidMachineConfiguration("KubeVirt credentials secret %s/%s: %v not found", namespace, secretName, getSecretErr)
		}
		return nil, getSecretErr
	}
	underKubeConfig, ok := userDataSecret.Data[UnderKubeConfig]
	if !ok {
		return nil, machineapiapierrors.InvalidMachineConfiguration("KubeVirt credentials secret %v did not contain key %v",
			secretName, UnderKubeConfig)
	}
	clientConfig, err := clientcmd.NewClientConfigFromBytes(underKubeConfig)
	if err != nil {
		return nil, err
	}
	kubecliclient, getClientErr := kubecli.GetKubevirtClientFromClientConfig(clientConfig)
	if getClientErr != nil {
		return nil, getClientErr
	}

	return &kubevirtClient{
		kubecliclient: kubecliclient,
		virtctlclient: "",
	}, nil
}

func (c *kubevirtClient) CreateVirtualMachine(namespace string, newVM *v1.VirtualMachine) (*v1.VirtualMachine, error) {
	return c.kubecliclient.VirtualMachine(namespace).Create(newVM)
}

func (c *kubevirtClient) DeleteVirtualMachine(namespace string, name string, options *k8smetav1.DeleteOptions) error {
	return c.kubecliclient.VirtualMachine(namespace).Delete(name, options)
}

func (c *kubevirtClient) GetVirtualMachine(namespace string, name string, options *k8smetav1.GetOptions) (*v1.VirtualMachine, error) {
	return c.kubecliclient.VirtualMachine(namespace).Get(name, options)
}

func (c *kubevirtClient) ListVirtualMachine(namespace string, options *k8smetav1.ListOptions) (*v1.VirtualMachineList, error) {
	return c.kubecliclient.VirtualMachine(namespace).List(options)
}

func (c *kubevirtClient) UpdateVirtualMachine(namespace string, vm *v1.VirtualMachine) (*v1.VirtualMachine, error) {
	return c.kubecliclient.VirtualMachine(namespace).Update(vm)
}

func (c *kubevirtClient) PatchVirtualMachine(namespace string, name string, pt types.PatchType, data []byte, subresources ...string) (result *v1.VirtualMachine, err error) {
	return c.kubecliclient.VirtualMachine(namespace).Patch(name, pt, data, subresources...)
}

func (c *kubevirtClient) RestartVirtualMachine(namespace string, name string) error {
	return c.kubecliclient.VirtualMachine(namespace).Restart(name)
}

func (c *kubevirtClient) StartVirtualMachine(namespace string, name string) error {
	return c.kubecliclient.VirtualMachine(namespace).Start(name)
}

func (c *kubevirtClient) StopVirtualMachine(namespace string, name string) error {
	return c.kubecliclient.VirtualMachine(namespace).Stop(name)
}

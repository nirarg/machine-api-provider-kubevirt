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

package infracluster

import (
	"github.com/openshift/cluster-api-provider-kubevirt/pkg/clients/tenantcluster"
	machineapiapierrors "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apimachineryerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtapiv1 "kubevirt.io/client-go/api/v1"
)

//go:generate mockgen -source=./client.go -destination=./mock/client_generated.go -package=mock

const (
	// platformCredentialsKey is secret key containing kubeconfig content of the infra-cluster
	platformCredentialsKey                  = "kubeconfig"
	defaultCredentialsSecretSecretName      = "kubevirt-credentials"
	defaultCredentialsSecretSecretNamespace = "openshift-machine-api"
)

// ClientBuilderFuncType is function type for building infra-cluster clients
type ClientBuilderFuncType func(tenantClusterKubernetesClient tenantcluster.Client, CredentialsSecretSecretName, namespace string) (Client, error)

// Client is a wrapper object for actual infra-cluster clients: kubernetes and the kubevirt
type Client interface {
	CreateVirtualMachine(namespace string, newVM *kubevirtapiv1.VirtualMachine) (*kubevirtapiv1.VirtualMachine, error)
	DeleteVirtualMachine(namespace string, name string, options *metav1.DeleteOptions) error
	GetVirtualMachine(namespace string, name string, options *metav1.GetOptions) (*kubevirtapiv1.VirtualMachine, error)
	GetVirtualMachineInstance(namespace string, name string, options *metav1.GetOptions) (*kubevirtapiv1.VirtualMachineInstance, error)
	ListVirtualMachine(namespace string, options metav1.ListOptions) (*kubevirtapiv1.VirtualMachineList, error)
	UpdateVirtualMachine(namespace string, vm *kubevirtapiv1.VirtualMachine) (*kubevirtapiv1.VirtualMachine, error)
	CreateSecret(namespace string, newSecret *corev1.Secret) (*corev1.Secret, error)
}

var (
	vmRes = schema.GroupVersionResource{
		Group:    kubevirtapiv1.GroupVersion.Group,
		Version:  kubevirtapiv1.GroupVersion.Version,
		Resource: "virtualmachines",
	}
	vmiRes = schema.GroupVersionResource{
		Group:    kubevirtapiv1.GroupVersion.Group,
		Version:  kubevirtapiv1.GroupVersion.Version,
		Resource: "virtualmachinesinstance",
	}
)

type client struct {
	kubernetesClient *kubernetes.Clientset
	dynamicClient    dynamic.Interface
}

// New creates our client wrapper object for the actual kubeVirt and kubernetes clients we use.
func New(tenantClusterKubernetesClient tenantcluster.Client, CredentialsSecretSecretName, namespace string) (Client, error) {
	CredentialsSecretSecretNamespace := namespace
	if CredentialsSecretSecretName == "" {
		CredentialsSecretSecretName = defaultCredentialsSecretSecretName
		CredentialsSecretSecretNamespace = defaultCredentialsSecretSecretNamespace
	}

	if namespace == "" {
		return nil, machineapiapierrors.InvalidMachineConfiguration("Infra-cluster credentials secret - Invalid empty namespace")
	}

	returnedSecret, err := tenantClusterKubernetesClient.GetSecret(CredentialsSecretSecretName, CredentialsSecretSecretNamespace)
	if err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return nil, machineapiapierrors.InvalidMachineConfiguration("Infra-cluster credentials secret %s/%s: %v not found", CredentialsSecretSecretNamespace, CredentialsSecretSecretName, err)
		}
		return nil, err
	}
	platformCredentials, ok := returnedSecret.Data[platformCredentialsKey]
	if !ok {
		return nil, machineapiapierrors.InvalidMachineConfiguration("Infra-cluster credentials secret %v did not contain key %v",
			CredentialsSecretSecretName, platformCredentials)
	}

	clientConfig, err := clientcmd.NewClientConfigFromBytes(platformCredentials)
	if err != nil {
		return nil, err
	}
	restClientConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	kubernetesClient, err := kubernetes.NewForConfig(restClientConfig)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(restClientConfig)
	if err != nil {
		return nil, err
	}
	return &client{
		kubernetesClient: kubernetesClient,
		dynamicClient:    dynamicClient,
	}, nil
}

func (c *client) CreateVirtualMachine(namespace string, newVM *kubevirtapiv1.VirtualMachine) (*kubevirtapiv1.VirtualMachine, error) {
	if err := c.createResource(newVM, namespace, vmRes); err != nil {
		return nil, err
	}
	return newVM, nil
}

func (c *client) DeleteVirtualMachine(namespace string, name string, options *metav1.DeleteOptions) error {
	return c.deleteResource(namespace, name, vmRes, options)
}

func (c *client) GetVirtualMachine(namespace string, name string, options *metav1.GetOptions) (*kubevirtapiv1.VirtualMachine, error) {
	resp, err := c.getResource(namespace, name, vmRes, options)
	if err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return nil, err
		}
		return nil, errors.Wrap(err, "failed to get VirtualMachine")
	}
	var vm kubevirtapiv1.VirtualMachine
	err = c.fromUnstructedToInterface(*resp, &vm, "VirtualMachine")
	return &vm, err
}

func (c *client) ListVirtualMachine(namespace string, options metav1.ListOptions) (*kubevirtapiv1.VirtualMachineList, error) {
	resp, err := c.listResource(namespace, vmRes, options)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list VirtualMachine")
	}
	var vmList kubevirtapiv1.VirtualMachineList
	err = c.fromUnstructedListToInterface(*resp, &vmList, "VirtualMachineList")
	return &vmList, err
}

func (c *client) UpdateVirtualMachine(namespace string, vm *kubevirtapiv1.VirtualMachine) (*kubevirtapiv1.VirtualMachine, error) {
	if err := c.updateResource(namespace, vm.Name, vmRes, vm); err != nil {
		return nil, err
	}
	return vm, nil
}

func (c *client) GetVirtualMachineInstance(namespace string, name string, options *metav1.GetOptions) (*kubevirtapiv1.VirtualMachineInstance, error) {
	resp, err := c.getResource(namespace, name, vmiRes, options)
	if err != nil {
		if apimachineryerrors.IsNotFound(err) {
			return nil, err
		}
		return nil, errors.Wrap(err, "failed to get VirtualMachineInstance")
	}
	var vmi kubevirtapiv1.VirtualMachineInstance
	err = c.fromUnstructedToInterface(*resp, &vmi, "VirtualMachineInstance")
	return &vmi, err
}

func (c *client) CreateSecret(namespace string, newSecret *corev1.Secret) (*corev1.Secret, error) {
	return c.kubernetesClient.CoreV1().Secrets(namespace).Create(newSecret)
}

func (c *client) createResource(obj interface{}, namespace string, resource schema.GroupVersionResource) error {
	resultMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to translate %s to Unstructed (for create operation)", resource.Resource)
	}
	input := unstructured.Unstructured{}
	input.SetUnstructuredContent(resultMap)
	resp, err := c.dynamicClient.Resource(resource).Namespace(namespace).Create(&input, metav1.CreateOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to create %s", resource.Resource)
	}
	unstructured := resp.UnstructuredContent()
	return runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured, obj)
}

func (c *client) getResource(namespace string, name string, resource schema.GroupVersionResource, options *metav1.GetOptions) (*unstructured.Unstructured, error) {
	return c.dynamicClient.Resource(resource).Namespace(namespace).Get(name, metav1.GetOptions{})
}

func (c *client) deleteResource(namespace string, name string, resource schema.GroupVersionResource, options *metav1.DeleteOptions) error {
	return c.dynamicClient.Resource(resource).Namespace(namespace).Delete(name, &metav1.DeleteOptions{})
}

func (c *client) listResource(namespace string, resource schema.GroupVersionResource, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return c.dynamicClient.Resource(resource).Namespace(namespace).List(opts)
}

func (c *client) updateResource(namespace string, name string, resource schema.GroupVersionResource, obj interface{}) error {
	resultMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to translate %s to Unstructed (for create operation)", resource.Resource)
	}
	input := unstructured.Unstructured{}
	input.SetUnstructuredContent(resultMap)
	resp, err := c.dynamicClient.Resource(resource).Namespace(namespace).Update(&input, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	unstructured := resp.UnstructuredContent()
	return runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured, obj)
}

func (c *client) fromUnstructedToInterface(src unstructured.Unstructured, dst interface{}, interfaceType string) error {
	unstructured := src.UnstructuredContent()
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured, dst); err != nil {
		return errors.Wrapf(err, "failed to translate unstructed to %s", interfaceType)
	}
	return nil
}

func (c *client) fromUnstructedListToInterface(src unstructured.UnstructuredList, dst interface{}, interfaceType string) error {
	unstructured := src.UnstructuredContent()
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructured, dst); err != nil {
		return errors.Wrapf(err, "failed to translate unstructed to %s", interfaceType)
	}
	return nil
}

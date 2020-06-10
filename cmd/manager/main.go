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

package main

import (
	"flag"
	"os"
	"time"

	"github.com/kubevirt/cluster-api-provider-kubevirt/pkg/actuator"
	kubernetesclient "github.com/kubevirt/cluster-api-provider-kubevirt/pkg/clients/kubernetes"
	kubevirtclient "github.com/kubevirt/cluster-api-provider-kubevirt/pkg/clients/kubevirt"
	"github.com/kubevirt/cluster-api-provider-kubevirt/pkg/managers/vm"
	mapiv1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	"github.com/openshift/machine-api-operator/pkg/controller/machine"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrl "sigs.k8s.io/controller-runtime/pkg/manager/signals"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

func main() {
	var printVersion bool
	flag.BoolVar(&printVersion, "version", false, "print version and exit")

	// TODO Add relevant flags as written in klog initFlags
	//klog.InitFlags(nil)

	watchNamespace := flag.String("namespace", "", "Namespace that the controller watches to reconcile machine-api objects. If unspecified, the controller watches for machine-api objects across all namespaces.")
	// TODO Remove this flag when stable
	flag.Set("logtostderr", "true")
	flag.Parse()

	// TODO what is the difference between this way to start the logger than the way it startes in aws?
	// ctrl.SetLogger(klogr.New())
	// setupLog := ctrl.Log.WithName("setup")
	log := logf.Log.WithName("kubevirt-controller-manager")
	logf.SetLogger(logf.ZapLogger(false))
	entryLog := log.WithName("entrypoint")

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		klog.Fatalf("Error getting configuration: %v", err)
	}

	// metricsAddr := flag.String("metrics-addr", ":8080", "The address the metric endpoint binds to.")
	// No need to setup "SyncPeriod: &syncPeriod" because there is no reconciled instance implemented
	syncPeriod := 10 * time.Minute
	opts := manager.Options{
		SyncPeriod: &syncPeriod,
		// Disable metrics serving
		MetricsBindAddress: "0",
	}
	if *watchNamespace != "" {
		opts.Namespace = *watchNamespace
		klog.Infof("Watching machine-api objects only in namespace %q for reconciliation.", opts.Namespace)
	}
	mgr, err := manager.New(cfg, opts)
	if err != nil {
		entryLog.Error(err, "Unable to set up overall controller manager")
		os.Exit(1)
	}

	// Initialize overKube kubernetes client
	kubernetesClient, err := kubernetesclient.New(mgr)
	if err != nil {
		entryLog.Error(err, "Failed to create kubernetes client from configuration")
	}

	// Setup Scheme for all resources
	// TODO do we need to use internal api.AddToScheme (for example in ovirt)
	if err := mapiv1beta1.AddToScheme(mgr.GetScheme()); err != nil {
		klog.Fatalf("Error setting up scheme: %v", err)
	}

	providerVM := vm.New(kubevirtclient.NewClient, kubernetesClient)
	// Initialize machine actuator.
	machineActuator := actuator.New(providerVM, mgr.GetEventRecorderFor("kubevirtcontroller"))

	// TODO this is call to machine-api-operator/pkg/controller/machine
	// In ovirt the call is to cluster-api/pkg/controller/machine
	// What is the difference? which one is better?
	if err := machine.AddWithActuator(mgr, machineActuator); err != nil {
		klog.Fatalf("Error adding actuator: %v", err)
	}

	//TODO Remove that line after finishing debugging
	entryLog.Info("@@@@@@@@@@@@@@@@ Before my changes")
	// Start the Cmd
	err = mgr.Start(ctrl.SetupSignalHandler())
	if err != nil {
		klog.Fatalf("Error starting manager: %v", err)
	}
}

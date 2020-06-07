# OpenShift cluster-api-provider-kubevirt

This repository hosts an implementation of a provider for KubeVirt for the
OpenShift [machine-api](https://github.com/openshift/cluster-api).

This provider runs as a machine-controller deployed by the
[machine-api-operator](https://github.com/openshift/machine-api-operator)

## Test locally built KubeVirt actuator

1. **Prepare The Openshift Cluster**

   Two options:
   1. Use Openshift cluster\
      In order to be able remove `machine-controller` container from `machine-api-controllers` deployment, need to remove `machine-api-operator` and `openshift-cluster-version` pods from the cluster, by decresing their replicacount in the deployments to zero:
      ```
      oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator\
      oc scale --replicas 0 -n openshift-machine-api deployments/machine-api-operator\
      ```
   2. Use CRC cluster

      Create CRC cluster, using the following instructions:\
      https://code-ready.github.io/crc/#introducing-codeready-containers_gsg\
      - Increase the default memory size provided by CRC (at list 20 Gib)
      - Use CRC in remote machine, not your private machine, use the following instructions for remote access:\
        https://www.openshift.com/blog/accessing-codeready-containers-on-a-remote-server/

      In the CRC, several operators have been disabled to lower the resource usage, `machine-api-operator` and `openshift-cluster-version` are among those operator, therefore, in order to deploy `machine-api-controllers` in the cluster, run `machine-api-operator` locally on your machine, using the instructions under:\
      https://github.com/openshift/machine-api-operator#dev

1. **Tear down machine-controller**

   Deployed machine API plane (`machine-api-controllers` deployment) is (among other
   controllers) running `machine-controller`.\
   In order to run locally built one, simply edit `machine-api-controllers` deployment and remove `machine-controller` container from it.

1. **Deploy secret with AWS credentials**

   AWS actuator assumes existence of a secret file (references in machine object) with base64 encoded credentials:

   ```yaml
   apiVersion: v1
   kind: Secret
   metadata:
      name: underkube-kubeconfig
      namespace: openshift-machine-api
   type: Opaque
   data:
      FILLIN
   ```

   You can use `hack/deploy-kubeconfig-secret.sh` script to generate the secret:
   ```sh
   ./hack/deploy-kubeconfig-secret.sh <kubeconfig file path>
   ```

1. **Build and run KubeVirt actuator outside of the cluster**

   ```sh
   $ make build
   ```

   ```sh
   $ ./bin/machine-controller-manager --kubeconfig <kubeconfig file path> --logtostderr -v 5 -alsologtostderr
   ```

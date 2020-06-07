#!/bin/bash

set -e

if [ $# -lt 1 ]; then
    echo "usage: $0 <kubeconfig_path>"
    exit 1
fi

enc_value=$( base64 -w 0 $1 )

echo -n "$enc_value"

secret_yaml="kubeconfig-secret.yaml"

if [[ ! -e $secret_yaml ]]; then
    touch $secret_yaml
fi

cat << EOF > $secret_yaml
apiVersion: v1
kind: Secret
metadata:
  name: underkube-kubeconfig
  namespace: openshift-machine-api
type: Opaque
data:
  $enc_value
EOF

oc create -f $secret_yaml

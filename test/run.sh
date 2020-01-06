#!/usr/bin/env bash

set -xe

kind create cluster --config=cluster.yaml --wait 5m
kubectl wait nodes --all --for condition=ready --timeout=180s
kubectl get nodes -o wide

kubectl apply -f ../dnslb-auth.yaml
if [ "$IMAGE" ]
then
  kind load docker-image $IMAGE
  PATCH=`cat <<EOF
  [
    {"op": "replace", "path": "/spec/template/spec/containers/0/image", "value": "$IMAGE"},
    {"op": "add", "path": "/spec/template/spec/containers/0/imagePullPolicy", "value": "Never"}
  ]
EOF
  `
  kubectl patch --local=true -f ../dnslb-deploy.yaml --type='json' -p="$PATCH" -o yaml | kubectl apply -f -
else
  kubectl apply -f ../dnslb-deploy.yaml
fi
kubectl wait deployment dnslb --for condition=available --timeout=180s

kubectl apply -f ../coredns.yaml
kubectl wait pod --selector name=coredns --for condition=ready --timeout=180s

if [ "$MANUAL" ]
then
  exit
fi

ANNOTATION="dnslb.loadworks.com/address"
DNS=`kubectl get nodes kind-worker -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'`
DOMAIN=${DOMAIN:-example.org}

kubectl apply -f nginx.yaml
kubectl wait deployment nginx --for condition=available --timeout=180s

EXPECTED=`kubectl get pod --selector name=nginx -o jsonpath='{.items[*].status.hostIP}'`
RECEIVED=`dig +short @$DNS nginx.$DOMAIN`
test "$EXPECTED" == "$RECEIVED"

EXPECTED=1.2.3.4
NODE=`kubectl get pod --selector name=nginx -o jsonpath='{.items[*].spec.nodeName}'`
kubectl annotate node $NODE $ANNOTATION=$EXPECTED
sleep 5
RECEIVED=`dig +short @$DNS nginx.$DOMAIN`
test "$EXPECTED" == "$RECEIVED"

EXPECTED=2
kubectl scale deployment nginx --replicas=$EXPECTED
kubectl wait deployment nginx --for condition=available --timeout=180s
RECEIVED=`dig +short @$DNS nginx.$DOMAIN | wc -l`
test "$EXPECTED" == "$RECEIVED"

if [ "$NOCLEANUP" ]
then
  exit
fi

kind delete cluster

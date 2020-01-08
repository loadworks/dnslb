# What is _dnslb_
It's a DNS-based Load Balancer for applications running on Kubernetes clusters.

# How it works

_dnslb_ runs a simple Kubernetes controller for the `LoadBalancer` services in Kubernetes clusters lacking this important feature (ie. mostly bare-metal environments). Instead of IP routing, it enables the use of DNS Load Balancing technique to direct client connections at the right nodes.

Technically, the _dnslb_ controller itself only updates the service status with the IP addresses of the nodes running the service pods. This information is then used by an actual DNS server to dynamically update its records with the provided addresses.

[CoreDNS](https://coredns.io/) is the recommended DNS server due to its incredible simplicity, robustness, and seamless integration through the built-in `kubernetes`, `k8s_external` and `loadbalance` plugins. A fairly short TTL should be applied to minimize the risk of clients using invalid cached records.

_CoreDNS_ doesn't actually provide true load balancing. It can only serve multiple DNS records in a round-robin fashion, as the `loadbalance` plugin doesn't currently support other methods. However, a more advanced DNS server can be used to enable record weighting by various load and capacity metrics, or an external plugin could provide that, but it's currently outside the scope of this project.

**Note:** Instead of hosting a DNS server in the cluster, additional automation can directly update the main DNS zone by communicating with its DNS server. The [External DNS](https://github.com/kubernetes-sigs/external-dns) project aims to provide this kind of service, but so far it's buggy and poorly maintained.

# Usage

## Deployment

To deploy _dnslb_ to your cluster in the `default` namespace:
```bash
# create the necessary RBAC resources:
kubectl apply -f dnslb-auth.yaml
# create the Deployment:
kubectl apply -f dnslb-deploy.yaml
```
Once deployed, it will manage the state of all `Service` objects of type `LoadBalancer` across all namespaces.

Deploying _CoreDNS_ as a `DaemonSet`:
- the placement (and number) of replicas can be limited with a node selector
- the provided example config works with just one domain and one namespace
- the variables from the `env` section are used only for expansion in the `Corefile`
- the rewrite rules allow to skip the namespace prefix
- to deploy the DNS server:
```bash
kubectl apply -f coredns.yaml
```
Once ready, the DNS server should return correct answers, reflecting the current service state. For a service `my-service` mapping to some running pod(s):
```bash
dig @10.20.30.40 my-service.default.example.org
# without the namespace prefix:
dig @10.20.30.40 my-service.example.org
# without the extra output:
dig +short @10.20.30.40 my-service.example.org
```
Where `10.20.30.40` is the DNS server IP address.
The returned answer should contain IP addresses of the nodes running these pods, with randomly changing record order.

## DNS configuration

In certain cases, the DNS server can be optionally configured to also function as a recursive resolver, so the client can set it in their network interface settings.

Otherwise, the global DNS infrastructure must be involved, so the glue records need to be set in the parent DNS zone. For each `#`'th DNS server instance (replica):
- create an `NS` record from `example.org` to `ns#.dns.example.org.`
- create an `A` record from `ns#.dns.example.org` to the instance IP address

If the DNS server IPs are publicly reachable, any user from the internet should be able to access the services.

## Customization

To override `my-node`'s ingress IP address used by _dnslb_ to `1.2.3.4` (eg. in case of multiple network interfaces):
```bash
kubectl annotate node my-node dnslb.loadworks.com/address=1.2.3.4
```

If the user wants to periodically re-generate the service state regardless of any triggering changes, an optional argument like `-sync 300` can be passed to the `dnslb` binary, specifying the forced reconciliation interval in seconds.

# Testing and development

The [test script](/test/run.sh) demonstrates the usage and provisions a safe playground in a local multi-node cluster via [Kind](https://kind.sigs.k8s.io/).
Run `NOCLEANUP=1 ./run.sh` to run the tests and retain the environment for further experiments, or `MANUAL=1 ./run.sh` to skip the tests and play in a clean environment.

To use an alternative, local image for _dnslb_, supply its name in the `IMAGE` variable. The default domain is `example.org` and can be overridden with the `DOMAIN` variable.

The test creates a simple `nginx` deployment with an accompanying service to demonstrate and verify the functionality.

The controller binary can be run locally (outside cluster) if supplied with the kubeconfig file:
```sh
go build
./dnslb -kubeconfig ~/.kube/config
```

# Limitations

DNS Load Balancing comes with some drawbacks, therefore it is crucial to understand them:
- clients have to access services via domain names
- the client's applications cannot cache resolved addresses
- the client's DNS cache cannot keep the records longer than their TTL
- the DNS server must be reachable by the DNS recursive resolver used by the client
- the DNS server must run on at least 2 independent nodes and must not jump to other nodes
- low TTL improves responsiveness to state changes, but also increases the DNS cache miss frequency, causing higher average connection delay and higher load on the DNS server

# Motivation

In a typical cloud-provided Kubernetes cluster, load balancing is achieved with IP routing, where each service gets a separate public IP address. The load balancer dynamically adjusts the network to route IP packets to the right nodes. The service IP address is usually put into an `A` record of the DNS server config by a cluster admin (or some automation), so the clients can use a nice name like `www.example.org`.

Unfortunately, core Kubernetes implementation doesn't provide the expected functionality for `LoadBalancer` services and it's up to each cloud provider to fill this gap with their own solution. That puts bare-metal clusters at a disadvantage, as load balancing requires an additional IP address pool and a specific network configuration in place. If the local network supports the BGP protocol (or L2 mode limitations are not an issue) and additional IP addresses are available, [MetalLB](https://metallb.universe.tf/) should be the way to go. Otherwise, `NodePort` service used to be the only option left, causing unbalanced network load, inefficient traffic forwarding between nodes and no failover whatsoever.

Luckily, now _dnslb_ can work around these issues by directing clients straight to the right nodes :)

# Deployment

## Container Image

The provider image is built as a multi-stage Docker image (Go 1.24 builder + distroless runtime) and pushed to GHCR:

```
ghcr.io/stuttgart-things/provider-clusterbook:<version>
ghcr.io/stuttgart-things/provider-clusterbook:latest
```

Each [GitHub release](https://github.com/stuttgart-things/provider-clusterbook/releases) publishes a semver-tagged image.

## Crossplane xpkg

The Crossplane package (xpkg) embeds the runtime image, CRDs, and package metadata. It is pushed to:

```
ghcr.io/stuttgart-things/provider-clusterbook-xpkg:<version>
ghcr.io/stuttgart-things/provider-clusterbook-xpkg:latest
```

### Prerequisites

- A Kubernetes cluster with [Crossplane](https://docs.crossplane.io/) installed
- `kubectl` configured to access the target cluster
- A running [clusterbook](https://github.com/stuttgart-things/clusterbook) REST API reachable from the cluster

### Install the Provider

Apply the Crossplane Provider manifest pointing to the published xpkg:

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-clusterbook
spec:
  package: ghcr.io/stuttgart-things/provider-clusterbook-xpkg:v0.1.0
```

```bash
kubectl apply -f - <<'EOF'
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-clusterbook
spec:
  package: ghcr.io/stuttgart-things/provider-clusterbook-xpkg:v0.1.0
EOF
```

Wait for the provider to become healthy:

```bash
kubectl wait provider.pkg.crossplane.io/provider-clusterbook \
  --for=condition=healthy --timeout=120s
```

### Verify Installation

```bash
# Check the provider is installed and healthy
kubectl get providers provider-clusterbook

# Expected output:
# NAME                   INSTALLED   HEALTHY   PACKAGE                                                     AGE
# provider-clusterbook   True        True      ghcr.io/stuttgart-things/provider-clusterbook-xpkg:v0.1.0   90s

# Check the provider pod is running
kubectl get pods -n crossplane-system | grep clusterbook

# Check that CRDs are registered
kubectl get crds | grep clusterbook
```

### Configure the Provider

Create a `ProviderConfig` (namespaced) or `ClusterProviderConfig` (cluster-wide) to set the clusterbook API URL:

```yaml
# Namespaced ProviderConfig
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ProviderConfig
metadata:
  name: default
  namespace: default
spec:
  url: "http://clusterbook.clusterbook-system:8080"
---
# Cluster-wide ProviderConfig
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  url: "http://clusterbook.clusterbook-system:8080"
```

Adjust the `url` to match your clusterbook deployment.

### Create an IPReservation

Reserve an IP from a clusterbook network pool with automatic DNS creation:

```yaml
apiVersion: ipreservation.clusterbook.stuttgart-things.com/v1alpha1
kind: IPReservation
metadata:
  name: my-cluster-ip
spec:
  forProvider:
    networkKey: "10.31.103"
    clusterName: "my-cluster"
    count: 1
    createDNS: true
  providerConfigRef:
    name: default
```

Or assign an explicit IP without DNS:

```yaml
apiVersion: ipreservation.clusterbook.stuttgart-things.com/v1alpha1
kind: IPReservation
metadata:
  name: my-cluster-ip-explicit
spec:
  forProvider:
    networkKey: "10.31.103"
    clusterName: "my-cluster"
    ip: "10.31.103.50"
    createDNS: false
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
```

Check the reservation status:

```bash
kubectl get ipreservation
kubectl describe ipreservation my-cluster-ip
```

### Uninstall

```bash
# Remove all IPReservation resources first
kubectl delete ipreservation --all

# Remove provider config
kubectl delete providerconfig.clusterbook.stuttgart-things.com default
# or
kubectl delete clusterproviderconfig.clusterbook.stuttgart-things.com default

# Remove the provider
kubectl delete provider.pkg.crossplane.io provider-clusterbook
```

## Local Development

```bash
# Create a kind cluster, install CRDs, and start the provider
make dev

# Or manually
kubectl apply -R -f package/crds
go run cmd/provider/main.go --debug

# Clean up
make dev-clean
```

## Provider Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` / `-d` | `false` | Enable debug logging |
| `--leader-election` / `-l` | `false` | Enable leader election for HA |
| `--poll` | `1m` | How often to check each resource for drift |
| `--sync` / `-s` | `1h` | Controller manager sync period |
| `--max-reconcile-rate` | `10` | Max reconciliations per second |

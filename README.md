# provider-clusterbook

`provider-clusterbook` is a [Crossplane](https://crossplane.io/) Provider that
manages IP address reservations from a [clusterbook](https://github.com/stuttgart-things/clusterbook)
REST API. It reserves IPs from network pools, optionally creates PowerDNS
wildcard DNS records, and exposes the assigned addresses in status.

## Features

- **IP reservation** — reserve one or more IPs from a clusterbook network pool
- **Explicit or auto-assign** — optionally specify an exact IP, or let clusterbook auto-assign from the pool
- **PowerDNS integration** — optionally create wildcard DNS records for reserved IPs via `createDNS`
- **Drift detection** — observes the clusterbook API and reconciles on changes
- **TLS support** — custom CA certificates and insecure skip-verify for self-signed endpoints

## Custom Resource Types

| Kind | Scope | Description |
|------|-------|-------------|
| `ProviderConfig` | Namespaced | Clusterbook API connection settings (namespaced) |
| `ClusterProviderConfig` | Cluster | Clusterbook API connection settings (cluster-scoped) |
| `IPReservation` | Cluster | Managed resource — reserves IPs from a network pool |

## Quick Start

### 1. Install the Provider

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-clusterbook
spec:
  package: ghcr.io/stuttgart-things/provider-clusterbook-xpkg:v3.1
```

### 2. Create a ClusterProviderConfig

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  url: "http://clusterbook.clusterbook.svc.cluster.local:8080"
```

For TLS endpoints with a custom CA:

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  url: "https://clusterbook.example.com"
  customCA: |
    -----BEGIN CERTIFICATE-----
    ...
    -----END CERTIFICATE-----
```

For self-signed certificates (development only):

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  url: "https://clusterbook.example.com"
  insecureSkipTLSVerify: true
```

### 3. Create an IPReservation

Auto-assign an IP from a network pool:

```yaml
apiVersion: ipreservation.clusterbook.stuttgart-things.com/v1alpha1
kind: IPReservation
metadata:
  name: my-cluster-ip
spec:
  forProvider:
    networkKey: "10.31.103"
    clusterName: my-cluster
    count: 1
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
```

Reserve a specific IP with DNS record:

```yaml
apiVersion: ipreservation.clusterbook.stuttgart-things.com/v1alpha1
kind: IPReservation
metadata:
  name: my-cluster-ip
spec:
  forProvider:
    networkKey: "10.31.103"
    clusterName: my-cluster
    ip: "10.31.103.42"
    createDNS: true
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
```

## Verify

```shell
$ kubectl get ipreservation
NAME             READY   SYNCED   NETWORK     CLUSTER      AGE
my-cluster-ip    True    True     10.31.103   my-cluster   5m

$ kubectl get ipreservation my-cluster-ip -o jsonpath='{.status.atProvider}' | jq
{
  "ipAddresses": ["10.31.103.42"],
  "status": "ASSIGNED:DNS"
}
```

## IPReservation Fields

### Spec (`forProvider`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `networkKey` | string | Yes | — | Network pool key (e.g. `10.31.103`) |
| `clusterName` | string | Yes | — | Cluster name to assign IPs to |
| `count` | integer | No | 1 | Number of IPs to reserve; lowering it releases the surplus |
| `ip` | string | No | — | Explicit IP, reserved first; fails if another cluster holds it or it is not in the pool |
| `createDNS` | boolean | No | false | Create a wildcard DNS record pointing at the explicit IP, else the first address |

### Status (`atProvider`)

| Field | Type | Description |
|-------|------|-------------|
| `ipAddresses` | []string | List of assigned IP addresses |
| `status` | string | Assignment status (`ASSIGNED` or `ASSIGNED:DNS`) |

## Building

### Prerequisites

- Go 1.23+
- Docker
- Make

### Build the Provider

```shell
# Initialize the build submodule (first time only)
make submodules

# Generate CRDs, deepcopy, and run linters
make reviewable

# Build the provider binary and Docker image
make build
```

### Running Tests

```shell
go test ./internal/... -v -count=1
```

## Project Structure

```
apis/
  v1alpha1/                    # ProviderConfig, ClusterProviderConfig types
  ipreservation/
    v1alpha1/                  # IPReservation managed resource type
internal/
  client/                      # Clusterbook REST API client
  controller/
    config/                    # ProviderConfig controller
    ipreservation/             # IPReservation reconciler
    clusterbook.go             # Controller registration
package/
  crds/                        # Generated CRDs
  crossplane.yaml              # Crossplane package metadata
```

## Provider Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` / `-d` | `false` | Enable debug logging |
| `--leader-election` / `-l` | `false` | Enable leader election for HA |
| `--poll` | `1m` | How often to check each resource for drift |
| `--sync` / `-s` | `1h` | Controller manager sync period |
| `--max-reconcile-rate` | `10` | Max reconciliations per second |

## Links

- [Crossplane Provider Development Guide](https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md)
- [clusterbook](https://github.com/stuttgart-things/clusterbook)

# provider-clusterbook

Crossplane Provider that manages IP reservations and optional PowerDNS records via the clusterbook REST API. It reserves/assigns IPs from network pools and can optionally create PDNS wildcard DNS records for reserved IPs.

## Features

- **IP reservation** -- reserves IPs from clusterbook network pools for Kubernetes clusters
- **Explicit IP assignment** -- optionally specify an exact IP address instead of auto-reserving
- **DNS record creation** -- optionally creates PowerDNS wildcard records for reserved IPs
- **Drift detection** -- polls the clusterbook API on every interval to verify IP assignments match desired state
- **IP release** -- releases IPs back to the pool and cleans up DNS records on deletion

## How It Works

When you create an `IPReservation` resource, the provider performs these steps:

1. **Connect** to the clusterbook REST API using the URL from the `ClusterProviderConfig`
2. **Reserve IPs** by calling `POST /api/v1/networks/{key}/reserve` once per address (the endpoint takes no count), starting from what the cluster already holds
3. **Populate status** with the assigned IP addresses and assignment status
4. **Poll for drift** on every interval by calling `GET /api/v1/networks/{key}/ips` and comparing assignments
5. **Release IPs** on deletion by calling `POST /api/v1/networks/{key}/release`

clusterbook saves an IP change before it touches DNS and reports the DNS outcome in the response. A reported DNS failure fails the reconcile, and the next reconcile re-asserts the record. Requires clusterbook v1.28.0 or later for `ip` to be honoured.

```
┌──────────────┐     ┌──────────────┐     ┌──────────────────────┐
│  Clusterbook │<───>│  Provider    │────>│  IPReservation       │
│  REST API    │     │  Controller  │     │  Status: ASSIGNED    │
└──────────────┘     │              │     │  IPs: [10.31.103.50] │
                     │              │     └──────────────────────┘
┌──────────────┐     │              │
│  Provider    │────>│              │
│  Config      │     │              │
│  (URL)       │     └──────────────┘
└──────────────┘
```

## Quick Start

### Step 1: Install the Provider

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-clusterbook
spec:
  package: ghcr.io/stuttgart-things/provider-clusterbook-xpkg:latest
```

### Step 2: Create a ClusterProviderConfig

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  url: "http://clusterbook.clusterbook-system:8080"
```

### Step 3: Create an IPReservation

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

### Step 4: Verify

```bash
$ kubectl get ipreservation
NAME             READY   SYNCED   NETWORK      CLUSTER       AGE
my-cluster-ip    True    True     10.31.103    my-cluster    5m
```

## Custom Resource Types

| Kind | Scope | API Group | Description |
|------|-------|-----------|-------------|
| `ProviderConfig` | Namespaced | `clusterbook.stuttgart-things.com` | Clusterbook API URL (namespaced) |
| `ClusterProviderConfig` | Cluster | `clusterbook.stuttgart-things.com` | Clusterbook API URL (cluster-scoped) |
| `IPReservation` | Cluster | `ipreservation.clusterbook.stuttgart-things.com` | Managed resource -- reserves IPs from network pools |

## IPReservation Spec Fields

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `networkKey` | yes | -- | Network pool key (e.g. `10.31.103`) |
| `clusterName` | yes | -- | Cluster to assign IPs to |
| `count` | no | `1` | Number of IPs to reserve; lowering it releases the surplus |
| `ip` | no | -- | Explicit IP, reserved first; fails if another cluster holds it or it is not in the pool |
| `createDNS` | no | `false` | Create a wildcard DNS record pointing at the explicit IP, else the first address |

## Clusterbook REST API Endpoints

| Operation | Method | Endpoint |
|-----------|--------|----------|
| Reserve IPs | `POST` | `/api/v1/networks/{key}/reserve` |
| Get IPs | `GET` | `/api/v1/networks/{key}/ips` |
| Update IP | `PUT` | `/api/v1/networks/{key}/ips/{ip}` |
| Release IPs | `POST` | `/api/v1/networks/{key}/release` |

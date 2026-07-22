# dr-network-check

Network connectivity checker for Data Foundation (DF) disaster recovery configurations.

## Overview

`dr-network-check` verifies network connectivity requirements across multi-cluster DF DR setups. It checks:

- **Ceph daemon ports**: Mon and OSD port reachability between ODF clusters (when CephCluster hostNetwork is enabled)
- **S3 routes**: S3 endpoint reachability from hub and client clusters
- **OCS provider server**: LoadBalancer, NodePort, and ClusterIP service connectivity between base clusters, including Submariner/GlobalNet validation
- **Proxy/noProxy**: Ensures all endpoints are properly configured in the cluster proxy settings

## Usage

### Generate configuration

```bash
dr-network-check init
```

This creates a `config.yaml` with placeholders. Edit it to set kubeconfig paths for your clusters.

### Run checks

```bash
dr-network-check check-network
```

Use `--config` to specify a custom config file:

```bash
dr-network-check check-network --config /path/to/config.yaml
```

## Configuration

```yaml
clusters:
  hub:
    kubeconfig: "/path/to/hub/kubeconfig"
  hub-passive:
    kubeconfig: ""
  c1:
    kubeconfig: "/path/to/c1/kubeconfig"
  c2:
    kubeconfig: "/path/to/c2/kubeconfig"
  c1-client-1:
    kubeconfig: ""
  c2-client-1:
    kubeconfig: ""

checks:
  skip-ceph-daemon-check: false
  skip-s3-check: false
  skip-provider-check: false

# test-pod-image: "registry.access.redhat.com/ubi9/ubi:latest"
```

| Field | Description |
|---|---|
| `hub` | Active ACM hub cluster (required) |
| `hub-passive` | Passive ACM hub cluster (optional) |
| `c1`, `c2` | ODF base clusters with rook/ceph (required) |
| `c1-client-1`, `c2-client-1` | Client clusters (optional, defaults to c1/c2) |
| `skip-ceph-daemon-check` | Skip mon/OSD port checks |
| `skip-s3-check` | Skip S3 route checks |
| `skip-provider-check` | Skip OCS provider server checks |
| `test-pod-image` | Override the image used for test pods |

## Building

```bash
# Local build
make

# Cross-platform builds
make cross

# Run tests
make test
```

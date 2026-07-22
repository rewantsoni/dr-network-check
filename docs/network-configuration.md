# Network Configuration for Regional DR with Data Foundation

This document describes the network requirements for a Regional Disaster Recovery (RDR) setup with Data Foundation (DF).

## Cluster topology

A typical RDR setup consists of:

| Cluster | Role | Description |
|---|---|---|
| **Hub** | ACM hub (active) | Manages DR policies, orchestrates failover |
| **Hub-passive** | ACM hub (passive) | Standby hub for hub-level DR (optional) |
| **c1** | Base cluster | Runs DF with rook/ceph, hosts OCS provider server |
| **c2** | Base cluster | Runs DF with rook/ceph, hosts OCS provider server |
| **c1-client-1** | Client cluster | Attached to c1 for storage (optional, defaults to c1) |
| **c2-client-1** | Client cluster | Attached to c2 for storage (optional, defaults to c2) |

The base clusters (c1, c2) replicate data between each other. Client clusters consume storage from their respective base cluster and fail over to the peer base cluster during DR.

## Network requirements

### 1. Host-network ports (mon/OSD)

When `hostNetwork: true` is set on the CephCluster CR (`ocs-storagecluster-cephcluster`), mon and OSD daemons bind directly to node IPs. Every storage node on c1 must be able to reach every storage node on c2 on the following ports:

| Daemon | Port | Protocol |
|---|---|---|
| Mon | 3300 or 6789 | TCP |
| OSD | Dynamically assigned (6800-7300) | TCP |

These ports must be open in any firewall or security group between the two clusters.

If `hostNetwork` is not enabled, ceph daemon port checks are skipped — ceph traffic stays within the cluster network. However, when Submariner with GlobalNet is enabled and `hostNetwork` is disabled, the tool verifies that the StorageCluster has `multiClusterService` configured (see [ClusterIP with Submariner](#clusterip-with-submariner)).

To check the CephCluster configuration:

```bash
oc get cephcluster ocs-storagecluster-cephcluster -n openshift-storage -o jsonpath='{.spec.network.hostNetwork}'
```

### 2. S3 routes

S3 routes in `openshift-storage` must be reachable from the hub, hub-passive, and all client clusters. These routes are OpenShift Routes exposing the Ceph Object Gateway (RGW) for object storage replication.

The following clusters need access to S3 routes on **both** c1 and c2:

- Hub (active)
- Hub (passive), if configured
- c1 / c1-client-1
- c2 / c2-client-1

To discover the S3 routes:

```bash
oc get routes s3 -n openshift-storage
```

### 3. OCS provider server

The `ocs-provider-server` service runs on each base cluster and exposes a gRPC endpoint (default port `50051`) over HTTPS. The peer base cluster communicates with this endpoint for storage provider operations.

#### Exported address annotation

If the StorageCluster has the annotation `ocs.openshift.io/api-server-exported-address`, the tool uses that value as the authoritative endpoint. This is the address the OCS operator is actually advertising to peers.

```bash
oc get storagecluster ocs-storagecluster -n openshift-storage \
  -o jsonpath='{.metadata.annotations.ocs\.openshift\.io/api-server-exported-address}'
```

When the annotation is present:

- The annotated endpoint is tested for reachability from the peer cluster — **FAIL** if unreachable
- The annotated endpoint is checked against the peer's noProxy configuration — **FAIL** if not covered
- Service discovery (LoadBalancer, NodePort, ClusterIP) is skipped since the annotation is definitive

When the annotation is absent, the tool falls back to discovering the endpoint from the `ocs-provider-server` service based on its type:

The service can be one of three types:

#### LoadBalancer

The service gets an external IP from the cloud provider's load balancer. This IP must be reachable from the peer cluster.

```bash
oc get svc ocs-provider-server -n openshift-storage
oc get svc ocs-provider-server-load-balancer -n openshift-storage
```

#### NodePort

The service is exposed on a static port on every node. The peer cluster connects via `<nodeIP>:<nodePort>`. The service CIDR of the provider cluster should be in the peer's noProxy configuration.

To find the NodePort:

```bash
oc get svc ocs-provider-server -n openshift-storage -o jsonpath='{.spec.ports[0].nodePort}'
```

#### ClusterIP with Submariner

When the service type is ClusterIP, cross-cluster connectivity is provided by Submariner. The behavior depends on whether GlobalNet is enabled.

**With GlobalNet enabled:**

- A `ServiceExport` must exist for `ocs-provider-server` in `openshift-storage`
- The endpoint is resolved via Submariner DNS: `<managedClusterName>.ocs-provider-server.openshift-storage.svc.clusterset.local`
- When `hostNetwork` is **not** enabled on the CephCluster, the StorageCluster must have `multiClusterService` configured:

  ```bash
  oc patch storagecluster ocs-storagecluster -n openshift-storage \
    --type merge \
    --patch '{"spec":{"network":{"multiClusterService":{"clusterID":"<managedClusterName>","enabled":true}}}}'
  ```

  The `clusterID` must match the ACM managed cluster name.

**Without GlobalNet:**

- ServiceExport is not required
- Submariner routes the ClusterIP directly
- The service and cluster CIDRs of the provider cluster must be in the peer's noProxy

To check if GlobalNet is enabled:

```bash
oc get submariner submariner -n submariner-operator -o jsonpath='{.spec.globalCIDR}'
```

A non-empty value indicates GlobalNet is enabled.

To find the managed cluster name:

```bash
oc get managedclusters -o wide   # on the ACM hub
```

### 4. Proxy / noProxy configuration

When a cluster-wide proxy is configured (`oc get proxy/cluster`), all DR endpoints must be added to `spec.noProxy` to ensure direct connectivity. Proxied connections to internal cluster endpoints will fail.

Endpoints that must be in noProxy:

| Endpoint type | Example | On which cluster's proxy |
|---|---|---|
| S3 route hostnames | `s3-openshift-storage.apps.c1.example.com` | Hub, hub-passive, all clients |
| Provider server LB IP | `10.48.101.50` | Peer base cluster |
| Provider server NodePort IPs | Node IPs of the provider cluster | Peer base cluster |
| Provider server Submariner endpoint | `<name>.ocs-provider-server.openshift-storage.svc.clusterset.local` | Peer base cluster |
|Provider Server - ClusterIP without GlobalNet (Cluster CIDRs) | `10.128.0.0/14` | Peer base cluster |

To view and edit the proxy configuration:

```bash
oc get proxy/cluster -o jsonpath='{.spec.noProxy}'
oc edit proxy/cluster
```

To get the cluster's network CIDRs:

```bash
oc get networks.config.openshift.io cluster -o jsonpath='{.spec.clusterNetwork[*].cidr}'
oc get networks.config.openshift.io cluster -o jsonpath='{.status.serviceNetwork[*]}'
```

### 5. Ceph toolbox

The OSD endpoint discovery requires the Ceph toolbox pod to be running. If it is not present, enable it:

```bash
oc patch storagecluster ocs-storagecluster -n openshift-storage \
  --type merge \
  --patch '{"spec":{"enableCephTools":true}}'
```

Wait for the `rook-ceph-tools` pod to become ready before running checks.

## Connectivity summary

```
                          ┌─────────────────────────────────────────────┐
                          │              ACM Hub Cluster                │
                          │                                             │
                          │  Manages DR policies, orchestrates failover │
                          └──────────┬──────────────────┬───────────────┘
                                     │                  │
                            S3 (HTTPS)                 S3 (HTTPS)
                                     │                  │
              ┌──────────────────────▼──┐          ┌────▼──────────────────────┐
              │     Base Cluster (c1)   │          │     Base Cluster (c2)     │
              │                         │          │                           │
              │  DF + Rook/Ceph         │          │  DF + Rook/Ceph           │
              │  OCS Provider Server    │          │  OCS Provider Server      │
              │  S3 Route               │          │  S3 Route                 │
              │                         │          │                           │
              │  Provider :50051 ◄──────┼──────────┼──────► Provider :50051    │
              │                         │  gRPC/   │                           │
              │  Mon :3300/:6789  ◄─────┼──HTTPS───┼────►  Mon :3300/:6789     │
              │  OSD :6800-7300   ◄─────┼──────────┼────►  OSD :6800-7300      │
              │  (if hostNetwork)       │  TCP     │  (if hostNetwork)         │
              └──────────▲──────────────┘          └───────────────▲───────────┘
                         │                                         │
                    S3 (HTTPS)                                S3 (HTTPS)
                         │                                         │
              ┌──────────┴──────────┐              ┌───────────────┴───────┐
              │  Client (c1-client) │              │  Client (c2-client)   │
              │                     │              │                       │
              │  Consumes storage   │              │  Consumes storage     │
              │  from c1            │              │  from c2              │
              └─────────────────────┘              └───────────────────────┘

  ─── Legend ──────────────────────────────────────────────────────────────

  S3 (HTTPS)       S3 object gateway route reachability
  Provider :50051  OCS provider server (LB / NodePort / ClusterIP+Submariner)
  Mon / OSD        Ceph daemon ports (only when hostNetwork is enabled)

  All endpoints must be in noProxy when a cluster-wide proxy is configured.
```

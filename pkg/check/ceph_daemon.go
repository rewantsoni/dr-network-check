package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
)

type daemonEndpoint struct {
	nodeName string
	nodeIP   string
	port     int32
	daemon   string
}

func CheckCephDaemonPorts(ctx context.Context, c1, c2, hub *cluster.Cluster) []CheckResult {
	console.Info("Checking ceph daemon port reachability between ODF clusters")

	hostNetC1, err := isHostNetworkEnabled(ctx, c1)
	if err != nil {
		console.Fail("Failed to check CephCluster hostNetwork on %s: %v", c1.Name, err)

		return []CheckResult{{
			Name: "cephcluster-hostnet-c1", Status: StatusFail,
			Message: fmt.Sprintf("Failed to check CephCluster hostNetwork on %s: %v", c1.Name, err),
		}}
	}

	hostNetC2, err := isHostNetworkEnabled(ctx, c2)
	if err != nil {
		console.Fail("Failed to check CephCluster hostNetwork on %s: %v", c2.Name, err)

		return []CheckResult{{
			Name: "cephcluster-hostnet-c2", Status: StatusFail,
			Message: fmt.Sprintf("Failed to check CephCluster hostNetwork on %s: %v", c2.Name, err),
		}}
	}

	if !hostNetC1 || !hostNetC2 {
		console.Info("hostNetwork is not enabled on all clusters — checking ceph daemon reachability via Submariner")

		var results []CheckResult

		if !hostNetC1 {
			console.Info("Checking %s ceph daemons reachable from %s", c1.Name, c2.Name)
			results = append(results, checkCephSubmariner(ctx, c1, c2, hub)...)
		}

		if !hostNetC2 {
			console.Info("Checking %s ceph daemons reachable from %s", c2.Name, c1.Name)
			results = append(results, checkCephSubmariner(ctx, c2, c1, hub)...)
		}

		return results
	}

	return checkCephHostNetwork(ctx, c1, c2)
}

func checkCephHostNetwork(ctx context.Context, c1, c2 *cluster.Cluster) []CheckResult {
	endpointsC1, err := discoverDaemonEndpoints(ctx, c1)
	if err != nil {
		console.Fail("Failed to discover daemon endpoints on %s: %v", c1.Name, err)

		return []CheckResult{{
			Name: "discover-daemons-c1", Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover daemon endpoints on %s: %v", c1.Name, err),
		}}
	}

	logEndpoints(c1.Name, endpointsC1)

	endpointsC2, err := discoverDaemonEndpoints(ctx, c2)
	if err != nil {
		console.Fail("Failed to discover daemon endpoints on %s: %v", c2.Name, err)

		return []CheckResult{{
			Name: "discover-daemons-c2", Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover daemon endpoints on %s: %v", c2.Name, err),
		}}
	}

	logEndpoints(c2.Name, endpointsC2)

	var results []CheckResult

	nodesC1, err := getStorageNodeIPs(ctx, c1.Clientset)
	if err != nil {
		console.Fail("Failed to list storage nodes on %s: %v", c1.Name, err)

		return []CheckResult{{
			Name: "list-nodes-c1", Status: StatusFail,
			Message: fmt.Sprintf("Failed to list storage nodes on %s: %v", c1.Name, err),
		}}
	}

	nodesC2, err := getStorageNodeIPs(ctx, c2.Clientset)
	if err != nil {
		console.Fail("Failed to list storage nodes on %s: %v", c2.Name, err)

		return []CheckResult{{
			Name: "list-nodes-c2", Status: StatusFail,
			Message: fmt.Sprintf("Failed to list storage nodes on %s: %v", c2.Name, err),
		}}
	}

	podsC1, err := deployPodsOnAllNodes(ctx, c1, nodesC1, "dr-check-hostnet-c1")
	if err != nil {
		console.Fail("Failed to deploy test pods on %s: %v", c1.Name, err)

		return []CheckResult{{
			Name: "deploy-pods-c1", Status: StatusFail,
			Message: fmt.Sprintf("Failed to deploy test pods on %s: %v", c1.Name, err),
		}}
	}

	defer cleanupPods(c1, podsC1)

	podsC2, err := deployPodsOnAllNodes(ctx, c2, nodesC2, "dr-check-hostnet-c2")
	if err != nil {
		console.Fail("Failed to deploy test pods on %s: %v", c2.Name, err)

		return []CheckResult{{
			Name: "deploy-pods-c2", Status: StatusFail,
			Message: fmt.Sprintf("Failed to deploy test pods on %s: %v", c2.Name, err),
		}}
	}

	defer cleanupPods(c2, podsC2)

	for _, srcNode := range nodesC1 {
		podName := podsC1[srcNode.name]

		for _, ep := range endpointsC2 {
			result := testDaemonPort(ctx, c1, podName, srcNode, ep)
			results = append(results, result)
			printResult(result)
		}
	}

	for _, srcNode := range nodesC2 {
		podName := podsC2[srcNode.name]

		for _, ep := range endpointsC1 {
			result := testDaemonPort(ctx, c2, podName, srcNode, ep)
			results = append(results, result)
			printResult(result)
		}
	}

	return results
}

func checkCephSubmariner(ctx context.Context, cl, peerCl, hub *cluster.Cluster) []CheckResult {
	if !cl.Submariner.Enabled {
		return nil
	}

	managedName, err := discoverManagedClusterName(ctx, hub, cl)
	if err != nil {
		console.Fail("Could not discover managed cluster name for %s: %v", cl.Name, err)

		return []CheckResult{{
			Name:    fmt.Sprintf("ceph-submariner-managed-name-%s", cl.Name),
			Status:  StatusFail,
			Message: fmt.Sprintf("Could not discover managed cluster name for %s from hub: %v", cl.Name, err),
		}}
	}

	var results []CheckResult

	if cl.Submariner.GlobalNet {
		console.Step("Submariner with GlobalNet on %s — checking multiClusterService and MCS reachability", cl.Name)
		results = append(results, checkMultiClusterService(ctx, cl, managedName)...)
		results = append(results, checkCephSubmarinerGlobalNet(ctx, cl, peerCl, managedName)...)
	} else {
		console.Step("Submariner without GlobalNet on %s — checking ClusterIP reachability and noProxy", cl.Name)
		results = append(results, CheckEndpointNoProxy(peerCl.Proxy, peerCl.Name, cl.Name, "ceph", gatherCephCIDRs(ctx, cl))...)
		results = append(results, checkCephSubmarinerClusterIP(ctx, cl, peerCl)...)
	}

	return results
}

func gatherCephCIDRs(ctx context.Context, cl *cluster.Cluster) []string {
	var hosts []string

	svcCIDRs, err := getServiceCIDR(ctx, cl)
	if err != nil {
		console.Warn("Could not get service CIDRs on %s: %v", cl.Name, err)
	}

	clusterCIDRs, err := getClusterCIDR(ctx, cl)
	if err != nil {
		console.Warn("Could not get cluster CIDRs on %s: %v", cl.Name, err)
	}

	hosts = append(hosts, svcCIDRs...)
	hosts = append(hosts, clusterCIDRs...)

	return hosts
}

func daemonEndpointIPs(endpoints []daemonEndpoint) []string {
	var ips []string

	for _, ep := range endpoints {
		ips = append(ips, ep.nodeIP)
	}

	return ips
}

func logEndpoints(clusterName string, endpoints []daemonEndpoint) {
	var mons, osds int

	for _, ep := range endpoints {
		if strings.HasPrefix(ep.daemon, "mon-") {
			mons++
		} else {
			osds++
		}
	}

	console.Step("Discovered %d mon endpoints and %d OSD endpoints on %s", mons, osds, clusterName)
}

func printResult(result CheckResult) {
	if result.Status == StatusPass {
		console.Pass("%s", result.Message)
	} else {
		console.Fail("%s", result.Message)
	}
}

func testDaemonTCP(ctx context.Context, cl *cluster.Cluster, podName, ip string, port int32, checkName, desc string) CheckResult {
	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("timeout 5 bash -c 'echo > /dev/tcp/%s/%d' 2>&1", ip, port),
	}

	_, _, err := ExecInPod(ctx, cl.RestConfig, cl.Clientset, podName, cl.Namespace, cmd)
	if err != nil {
		return CheckResult{
			Name:    checkName,
			Status:  StatusFail,
			Message: fmt.Sprintf("%s — unreachable", desc),
		}
	}

	return CheckResult{
		Name:    checkName,
		Status:  StatusPass,
		Message: desc,
	}
}

func testDaemonPort(ctx context.Context, srcCluster *cluster.Cluster, podName string,
	srcNode nodeInfo, ep daemonEndpoint,
) CheckResult {
	return testDaemonTCP(ctx, srcCluster, podName,
		ep.nodeIP, ep.port,
		fmt.Sprintf("%s-port-%d:%s(%s)->%s(%s)",
			ep.daemon, ep.port, srcCluster.Name, srcNode.ip, ep.nodeName, ep.nodeIP),
		fmt.Sprintf("%s port %d: %s (%s) -> %s (%s)",
			ep.daemon, ep.port, srcNode.name, srcNode.ip, ep.nodeName, ep.nodeIP),
	)
}

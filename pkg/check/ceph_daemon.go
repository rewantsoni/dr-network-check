package check

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const storageNodeLabel = "cluster.ocs.openshift.io/openshift-storage"

var cephClusterGVR = schema.GroupVersionResource{
	Group:    "ceph.rook.io",
	Version:  "v1",
	Resource: "cephclusters",
}

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
		console.Info("hostNetwork is not enabled on all clusters — skipping ceph daemon port checks")
		var results []CheckResult
		if !hostNetC1 {
			results = append(results, checkCephMCS(ctx, c1, hub)...)
		}
		if !hostNetC2 {
			results = append(results, checkCephMCS(ctx, c2, hub)...)
		}
		return results
	}

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

	var results []CheckResult

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

func isHostNetworkEnabled(ctx context.Context, cl *cluster.Cluster) (bool, error) {
	dynClient, err := dynamic.NewForConfig(cl.RestConfig)
	if err != nil {
		return false, fmt.Errorf("creating dynamic client: %w", err)
	}

	obj, err := dynClient.Resource(cephClusterGVR).Namespace(storageNamespace).Get(
		ctx, "ocs-storagecluster-cephcluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("getting CephCluster: %w", err)
	}

	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	network, ok := spec["network"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	hostNetwork, _ := network["hostNetwork"].(bool)

	return hostNetwork, nil
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

func discoverDaemonEndpoints(ctx context.Context, cl *cluster.Cluster) ([]daemonEndpoint, error) {
	monEPs, err := discoverMonEndpoints(ctx, cl.Clientset)
	if err != nil {
		return nil, fmt.Errorf("discovering mon endpoints: %w", err)
	}

	osdEPs, err := discoverOSDEndpoints(ctx, cl)
	if err != nil {
		return nil, fmt.Errorf("discovering OSD endpoints: %w", err)
	}

	endpoints := append(monEPs, osdEPs...)
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no mon or OSD endpoints found on %s", cl.Name)
	}

	return endpoints, nil
}

func discoverMonEndpoints(ctx context.Context, clientset kubernetes.Interface) ([]daemonEndpoint, error) {
	cm, err := clientset.CoreV1().ConfigMaps(storageNamespace).Get(
		ctx, "rook-ceph-mon-endpoints", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting rook-ceph-mon-endpoints ConfigMap: %w", err)
	}

	data := cm.Data["data"]
	if data == "" {
		return nil, fmt.Errorf("rook-ceph-mon-endpoints ConfigMap has empty data field")
	}

	nodeMap := resolveMonNodes(cm.Data["mapping"])

	var endpoints []daemonEndpoint

	for _, entry := range strings.Split(data, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}

		monID := parts[0]
		addrPort := parts[1]

		ip, port, err := parseAddress(addrPort)
		if err != nil {
			continue
		}

		nodeName := nodeMap[monID]
		if nodeName == "" {
			nodeName = fmt.Sprintf("unknown-node-%s", monID)
		}

		endpoints = append(endpoints, daemonEndpoint{
			nodeName: nodeName,
			nodeIP:   ip,
			port:     port,
			daemon:   fmt.Sprintf("mon-%s", monID),
		})
	}

	return endpoints, nil
}

type monMapping struct {
	Node map[string]struct {
		Name    string `json:"Name"`
		Address string `json:"Address"`
	} `json:"node"`
}

func resolveMonNodes(mappingJSON string) map[string]string {
	result := map[string]string{}
	if mappingJSON == "" {
		return result
	}

	var mapping monMapping
	if err := json.Unmarshal([]byte(mappingJSON), &mapping); err != nil {
		return result
	}

	for monID, node := range mapping.Node {
		result[monID] = node.Name
	}

	return result
}

func discoverOSDEndpoints(ctx context.Context, cl *cluster.Cluster) ([]daemonEndpoint, error) {
	toolboxPod, err := findToolboxPod(ctx, cl.Clientset)
	if err != nil {
		return nil, err
	}

	stdout, stderr, err := ExecInPodContainer(ctx, cl.RestConfig, cl.Clientset,
		toolboxPod, storageNamespace, "rook-ceph-tools",
		[]string{"ceph", "osd", "dump", "--format", "json"})
	if err != nil {
		return nil, fmt.Errorf("running ceph osd dump in toolbox: %w (stderr: %s)", err, stderr)
	}

	return parseOSDDump(ctx, stdout, cl.Clientset)
}

func findToolboxPod(ctx context.Context, clientset kubernetes.Interface) (string, error) {
	pods, err := clientset.CoreV1().Pods(storageNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=rook-ceph-tools",
	})
	if err != nil {
		return "", err
	}

	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}

	return "", fmt.Errorf(
		"no running rook-ceph-tools pod found in %s namespace. "+
			"Enable the Ceph toolbox by running:\n"+
			"    oc patch storagecluster ocs-storagecluster -n %s "+
			"--type merge --patch '{\"spec\":{\"enableCephTools\":true}}'",
		storageNamespace, storageNamespace)
}

type osdDump struct {
	OSDs []osdInfo `json:"osds"`
}

type osdInfo struct {
	OSD        int    `json:"osd"`
	PublicAddr string `json:"public_addr"`
}

func parseOSDDump(ctx context.Context, jsonData string, clientset kubernetes.Interface) ([]daemonEndpoint, error) {
	var dump osdDump
	if err := json.Unmarshal([]byte(jsonData), &dump); err != nil {
		return nil, fmt.Errorf("parsing ceph osd dump: %w", err)
	}

	osdNodeMap, err := buildOSDNodeMap(ctx, clientset)
	if err != nil {
		return nil, err
	}

	var endpoints []daemonEndpoint

	for _, osd := range dump.OSDs {
		ip, port, err := parseAddress(osd.PublicAddr)
		if err != nil {
			continue
		}

		nodeName := osdNodeMap[ip]
		if nodeName == "" {
			nodeName = fmt.Sprintf("unknown-node-osd-%d", osd.OSD)
		}

		endpoints = append(endpoints, daemonEndpoint{
			nodeName: nodeName,
			nodeIP:   ip,
			port:     port,
			daemon:   fmt.Sprintf("osd-%d", osd.OSD),
		})
	}

	return endpoints, nil
}

func buildOSDNodeMap(ctx context.Context, clientset kubernetes.Interface) (map[string]string, error) {
	pods, err := clientset.CoreV1().Pods(storageNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=rook-ceph-osd",
	})
	if err != nil {
		return nil, err
	}

	ipToNode := map[string]string{}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.HostIP != "" {
			ipToNode[pod.Status.HostIP] = pod.Spec.NodeName
		}
	}

	return ipToNode, nil
}

func parseAddress(addr string) (string, int32, error) {
	if idx := strings.Index(addr, "/"); idx != -1 {
		addr = addr[:idx]
	}

	host, portStr, found := strings.Cut(addr, ":")
	if !found {
		return "", 0, fmt.Errorf("invalid address %q: no colon", addr)
	}

	port, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}

	return host, int32(port), nil
}

func testDaemonPort(ctx context.Context, srcCluster *cluster.Cluster, podName string,
	srcNode nodeInfo, ep daemonEndpoint,
) CheckResult {
	name := fmt.Sprintf("%s-port-%d:%s(%s)->%s(%s)",
		ep.daemon, ep.port, srcCluster.Name, srcNode.ip, ep.nodeName, ep.nodeIP)

	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("timeout 5 bash -c 'echo > /dev/tcp/%s/%d' 2>&1", ep.nodeIP, ep.port),
	}

	_, _, err := ExecInPod(ctx, srcCluster.RestConfig, srcCluster.Clientset, podName, srcCluster.Namespace, cmd)
	if err != nil {
		return CheckResult{
			Name:   name,
			Status: StatusFail,
			Message: fmt.Sprintf("%s port %d: %s (%s) -> %s (%s) - unreachable",
				ep.daemon, ep.port, srcNode.name, srcNode.ip, ep.nodeName, ep.nodeIP),
		}
	}

	return CheckResult{
		Name:   name,
		Status: StatusPass,
		Message: fmt.Sprintf("%s port %d: %s (%s) -> %s (%s)",
			ep.daemon, ep.port, srcNode.name, srcNode.ip, ep.nodeName, ep.nodeIP),
	}
}

func deployPodsOnAllNodes(ctx context.Context, cl *cluster.Cluster, nodes []nodeInfo, prefix string) (map[string]string, error) {
	podNames := map[string]string{}

	for i, node := range nodes {
		podName := fmt.Sprintf("%s-%d", prefix, i)
		console.Step("Deploying test pod on %s (node: %s)", cl.Name, node.name)

		if _, err := DeployTestPod(ctx, cl.Clientset, podName, cl.Namespace, true, node.name); err != nil {
			return podNames, fmt.Errorf("node %s: %w", node.name, err)
		}

		podNames[node.name] = podName
	}

	for nodeName, podName := range podNames {
		if err := WaitForPodReady(ctx, cl.Clientset, podName, cl.Namespace); err != nil {
			return podNames, fmt.Errorf("pod on node %s not ready: %w", nodeName, err)
		}
	}

	return podNames, nil
}

func cleanupPods(cl *cluster.Cluster, podNames map[string]string) {
	console.Step("Cleaning up test pods on %s", cl.Name)

	for _, podName := range podNames {
		_ = DeleteTestPod(context.Background(), cl.Clientset, podName, cl.Namespace)
	}
}

func printResult(result CheckResult) {
	if result.Status == StatusPass {
		console.Pass("%s", result.Message)
	} else {
		console.Fail("%s", result.Message)
	}
}

type nodeInfo struct {
	name string
	ip   string
}

func getStorageNodeIPs(ctx context.Context, clientset kubernetes.Interface) ([]nodeInfo, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: storageNodeLabel,
	})
	if err != nil {
		return nil, err
	}

	var storageNodes []nodeInfo

	for _, node := range nodes.Items {
		ip := getNodeInternalIP(&node)
		if ip != "" {
			storageNodes = append(storageNodes, nodeInfo{name: node.Name, ip: ip})
		}
	}

	if len(storageNodes) == 0 {
		return nil, fmt.Errorf("no nodes with label %s found", storageNodeLabel)
	}

	return storageNodes, nil
}

func getNodeInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}

	return ""
}

func checkMultiClusterService(ctx context.Context, cl *cluster.Cluster, managedClusterName string) []CheckResult {
	dynClient, err := dynamic.NewForConfig(cl.RestConfig)
	if err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("multicluster-svc-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to create dynamic client on %s: %v", cl.Name, err),
		}}
	}

	obj, err := dynClient.Resource(storageClusterGVR).Namespace(storageNamespace).Get(
		ctx, "ocs-storagecluster", metav1.GetOptions{})
	if err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("multicluster-svc-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get StorageCluster on %s: %v", cl.Name, err),
		}}
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	if spec == nil {
		return mcsNotConfigured(cl.Name, managedClusterName)
	}

	network, _ := spec["network"].(map[string]interface{})
	if network == nil {
		return mcsNotConfigured(cl.Name, managedClusterName)
	}

	mcs, _ := network["multiClusterService"].(map[string]interface{})
	if mcs == nil {
		return mcsNotConfigured(cl.Name, managedClusterName)
	}

	enabled, _ := mcs["enabled"].(bool)
	clusterID, _ := mcs["clusterID"].(string)

	if !enabled {
		console.Warn("multiClusterService is not enabled on %s", cl.Name)

		return mcsNotConfigured(cl.Name, managedClusterName)
	}

	if clusterID == "" {
		console.Warn("multiClusterService on %s has no clusterID set", cl.Name)

		return []CheckResult{{
			Name: fmt.Sprintf("multicluster-svc-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("multiClusterService on %s has no clusterID set — "+
				"patch StorageCluster:\n"+
				"    oc patch storagecluster ocs-storagecluster -n %s "+
				"--type merge --patch '{\"spec\":{\"network\":{\"multiClusterService\":{\"clusterID\":\"%s\",\"enabled\":true}}}}'",
				cl.Name, storageNamespace, managedClusterName),
		}}
	}

	if clusterID != managedClusterName {
		console.Warn("multiClusterService clusterID on %s is %q, expected %q", cl.Name, clusterID, managedClusterName)

		return []CheckResult{{
			Name: fmt.Sprintf("multicluster-svc-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("multiClusterService clusterID on %s is %q, expected managed cluster name %q",
				cl.Name, clusterID, managedClusterName),
		}}
	}

	console.Pass("multiClusterService is configured on %s (clusterID: %s)", cl.Name, clusterID)

	return []CheckResult{{
		Name: fmt.Sprintf("multicluster-svc-%s", cl.Name), Status: StatusPass,
		Message: fmt.Sprintf("multiClusterService is configured on %s (clusterID: %s)", cl.Name, clusterID),
	}}
}

func mcsNotConfigured(clName, managedClusterName string) []CheckResult {
	console.Warn("multiClusterService is not configured on %s", clName)

	return []CheckResult{{
		Name: fmt.Sprintf("multicluster-svc-%s", clName), Status: StatusWarn,
		Message: fmt.Sprintf("multiClusterService is not configured on %s — "+
			"patch StorageCluster:\n"+
			"    oc patch storagecluster ocs-storagecluster -n %s "+
			"--type merge --patch '{\"spec\":{\"network\":{\"multiClusterService\":{\"clusterID\":\"%s\",\"enabled\":true}}}}'",
			clName, storageNamespace, managedClusterName),
	}}
}

func checkCephMCS(ctx context.Context, cl, hub *cluster.Cluster) []CheckResult {
	globalNet, gnResult := isGlobalNetEnabled(ctx, cl)
	if gnResult != nil {
		return []CheckResult{*gnResult}
	}

	if !globalNet {
		return nil
	}

	console.Step("hostNetwork disabled but GlobalNet enabled on %s — checking multiClusterService", cl.Name)

	managedName, err := discoverManagedClusterName(ctx, hub, cl)
	if err != nil {
		console.Fail("Could not discover managed cluster name for %s: %v", cl.Name, err)

		return []CheckResult{{
			Name:    fmt.Sprintf("ceph-mcs-managed-name-%s", cl.Name),
			Status:  StatusFail,
			Message: fmt.Sprintf("Could not discover managed cluster name for %s from hub: %v", cl.Name, err),
		}}
	}

	return checkMultiClusterService(ctx, cl, managedName)
}

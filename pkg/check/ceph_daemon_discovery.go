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

type nodeInfo struct {
	name string
	ip   string
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

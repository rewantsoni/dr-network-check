package check

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const (
	providerServiceName        = "ocs-provider-server"
	providerLBName             = "ocs-provider-server-load-balancer"
	exportedAddressAnnotation  = "ocs.openshift.io/api-server-exported-address"
	defaultProviderPort        = int32(50051)
)

var (
	serviceExportGVR = schema.GroupVersionResource{
		Group:    "multicluster.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "serviceexports",
	}
	managedClusterGVR = schema.GroupVersionResource{
		Group:    "cluster.open-cluster-management.io",
		Version:  "v1",
		Resource: "managedclusters",
	}
	submarinerGVR = schema.GroupVersionResource{
		Group:    "submariner.io",
		Version:  "v1alpha1",
		Resource: "submariners",
	}
	storageClusterGVR = schema.GroupVersionResource{
		Group:    "ocs.openshift.io",
		Version:  "v1",
		Resource: "storageclusters",
	}
)

type providerEndpoint struct {
	host    string
	port    int32
	service string
}

func CheckOCSProvider(ctx context.Context, clusters *cluster.Clusters) []CheckResult {
	console.Info("Checking OCS provider server connectivity")

	var results []CheckResult

	r := checkProviderServices(ctx, clusters.C1, clusters.C2, clusters.Hub)
	results = append(results, r...)

	r = checkProviderServices(ctx, clusters.C2, clusters.C1, clusters.Hub)
	results = append(results, r...)

	return results
}

func checkProviderServices(ctx context.Context, cl, peerCl, hub *cluster.Cluster) []CheckResult {
	var results []CheckResult

	exportedAddr, err := getExportedAddress(ctx, cl)
	if err != nil {
		console.Warn("Could not check StorageCluster annotation on %s: %v", cl.Name, err)
	}

	if exportedAddr != "" {
		console.Step("Found exported address on %s: %s", cl.Name, exportedAddr)
		host, port, svc := parseExportedAddress(exportedAddr)
		endpoints := []providerEndpoint{{host: host, port: port, service: svc}}

		proxyResults := checkProviderNoProxy(ctx, peerCl, cl.Name, []string{host})
		results = append(results, proxyResults...)

		reachResults := testProviderReachability(ctx, peerCl, cl.Name, endpoints)
		results = append(results, reachResults...)

		return results
	}

	var endpoints []providerEndpoint
	var noProxyHosts []string

	lbResults, lbEPs := checkProviderLB(ctx, cl)
	results = append(results, lbResults...)
	endpoints = append(endpoints, lbEPs...)

	svcResults, svcEPs, svcNoProxyHosts := checkProviderService(ctx, cl, hub)
	results = append(results, svcResults...)
	endpoints = append(endpoints, svcEPs...)
	noProxyHosts = append(noProxyHosts, svcNoProxyHosts...)

	for _, ep := range endpoints {
		noProxyHosts = append(noProxyHosts, ep.host)
	}

	if len(noProxyHosts) > 0 {
		proxyResults := checkProviderNoProxy(ctx, peerCl, cl.Name, noProxyHosts)
		results = append(results, proxyResults...)
	}

	if len(endpoints) > 0 {
		reachResults := testProviderReachability(ctx, peerCl, cl.Name, endpoints)
		results = append(results, reachResults...)
	}

	return results
}

func checkProviderLB(ctx context.Context, cl *cluster.Cluster) ([]CheckResult, []providerEndpoint) {
	svc, err := cl.Clientset.CoreV1().Services(storageNamespace).Get(
		ctx, providerLBName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}

		return []CheckResult{{
			Name: fmt.Sprintf("provider-lb-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get %s on %s: %v", providerLBName, cl.Name, err),
		}}, nil
	}

	console.Step("Found %s on %s (type: %s)", providerLBName, cl.Name, svc.Spec.Type)

	host := getLoadBalancerHost(svc)
	if host == "" {
		console.Warn("%s on %s has no IP assigned (pending)", providerLBName, cl.Name)

		return []CheckResult{{
			Name: fmt.Sprintf("provider-lb-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("%s on %s has no IP assigned (LoadBalancer pending)", providerLBName, cl.Name),
		}}, nil
	}

	port := getServicePort(svc)
	console.Step("%s on %s endpoint: %s:%d", providerLBName, cl.Name, host, port)

	return nil, []providerEndpoint{
		{host: host, port: port, service: providerLBName},
	}
}

func checkProviderService(ctx context.Context, cl, hub *cluster.Cluster) ([]CheckResult, []providerEndpoint, []string) {
	svc, err := cl.Clientset.CoreV1().Services(storageNamespace).Get(
		ctx, providerServiceName, metav1.GetOptions{})
	if err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("provider-svc-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get %s on %s: %v", providerServiceName, cl.Name, err),
		}}, nil, nil
	}

	console.Step("Found %s on %s (type: %s)", providerServiceName, cl.Name, svc.Spec.Type)

	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		r, ep := checkProviderSvcLoadBalancer(ctx, cl, svc)
		return r, ep, nil
	case corev1.ServiceTypeNodePort:
		return checkProviderSvcNodePort(ctx, cl, svc)
	case corev1.ServiceTypeClusterIP:
		return checkProviderSvcClusterIP(ctx, cl, hub, svc)
	default:
		return []CheckResult{{
			Name: fmt.Sprintf("provider-svc-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("%s on %s has unexpected type: %s", providerServiceName, cl.Name, svc.Spec.Type),
		}}, nil, nil
	}
}

func checkProviderSvcLoadBalancer(ctx context.Context, cl *cluster.Cluster, svc *corev1.Service) ([]CheckResult, []providerEndpoint) {
	host := getLoadBalancerHost(svc)
	if host == "" {
		console.Warn("%s on %s has no IP assigned (pending)", providerServiceName, cl.Name)

		return []CheckResult{{
			Name: fmt.Sprintf("provider-svc-lb-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("%s on %s has no IP assigned (LoadBalancer pending)", providerServiceName, cl.Name),
		}}, nil
	}

	port := getServicePort(svc)
	console.Step("%s on %s endpoint: %s:%d", providerServiceName, cl.Name, host, port)

	return nil, []providerEndpoint{
		{host: host, port: port, service: providerServiceName},
	}
}

func checkProviderSvcNodePort(ctx context.Context, cl *cluster.Cluster, svc *corev1.Service) ([]CheckResult, []providerEndpoint, []string) {
	var results []CheckResult
	var noProxyHosts []string

	nodePort := getNodePort(svc)
	if nodePort == 0 {
		return []CheckResult{{
			Name: fmt.Sprintf("provider-svc-np-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("%s on %s has no NodePort assigned", providerServiceName, cl.Name),
		}}, nil, nil
	}

	serviceCIDRs, err := getServiceCIDR(ctx, cl)
	if err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("provider-svc-cidr-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("Could not get service CIDR on %s: %v", cl.Name, err),
		})
	} else {
		noProxyHosts = append(noProxyHosts, serviceCIDRs...)

		for _, cidr := range serviceCIDRs {
			console.Step("%s on %s: service CIDR %s should be in peer noProxy", providerServiceName, cl.Name, cidr)
		}
	}

	nodes, err := getStorageNodeIPs(ctx, cl.Clientset)
	if err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("provider-svc-nodes-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get storage nodes on %s for NodePort check: %v", cl.Name, err),
		})

		return results, nil, noProxyHosts
	}

	var endpoints []providerEndpoint

	for _, node := range nodes {
		endpoints = append(endpoints, providerEndpoint{
			host: node.ip, port: nodePort, service: fmt.Sprintf("%s-nodeport", providerServiceName),
		})
	}

	return results, endpoints, noProxyHosts
}

func checkProviderSvcClusterIP(ctx context.Context, cl, hub *cluster.Cluster, svc *corev1.Service) ([]CheckResult, []providerEndpoint, []string) {
	var results []CheckResult

	globalNet, gnResult := isGlobalNetEnabled(ctx, cl)
	if gnResult != nil {
		results = append(results, *gnResult)
	}

	port := getServicePort(svc)

	if globalNet {
		console.Step("GlobalNet is enabled on %s — ServiceExport required", cl.Name)

		seResult := checkServiceExport(ctx, cl)
		results = append(results, seResult)

		managedName, err := discoverManagedClusterName(ctx, hub, cl)
		if err != nil {
			console.Fail("Could not discover managed cluster name for %s: %v", cl.Name, err)

			results = append(results, CheckResult{
				Name: fmt.Sprintf("provider-managed-name-%s", cl.Name), Status: StatusFail,
				Message: fmt.Sprintf("Could not discover managed cluster name for %s from hub: %v", cl.Name, err),
			})

			return results, nil, nil
		}

		console.Step("Managed cluster name for %s: %s", cl.Name, managedName)

		hostNet, _ := isHostNetworkEnabled(ctx, cl)
		if !hostNet {
			mcsResults := checkMultiClusterService(ctx, cl, managedName)
			results = append(results, mcsResults...)
		}

		endpoint := fmt.Sprintf("%s.%s.%s.svc.clusterset.local", managedName, providerServiceName, storageNamespace)
		console.Step("%s on %s submariner endpoint: %s:%d", providerServiceName, cl.Name, endpoint, port)

		return results, []providerEndpoint{
			{host: endpoint, port: port, service: fmt.Sprintf("%s-submariner", providerServiceName)},
		}, nil
	}

	console.Step("GlobalNet is not enabled on %s — using ClusterIP directly", cl.Name)

	clusterIP := svc.Spec.ClusterIP
	if clusterIP == "" || clusterIP == "None" {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("provider-clusterip-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("%s on %s has no ClusterIP assigned", providerServiceName, cl.Name),
		})

		return results, nil, nil
	}

	console.Step("%s on %s ClusterIP endpoint: %s:%d", providerServiceName, cl.Name, clusterIP, port)

	var noProxyHosts []string

	serviceCIDRs, err := getServiceCIDR(ctx, cl)
	if err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("provider-svc-cidr-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("Could not get service CIDR on %s: %v", cl.Name, err),
		})
	} else {
		noProxyHosts = append(noProxyHosts, serviceCIDRs...)

		for _, cidr := range serviceCIDRs {
			console.Step("%s on %s: service CIDR %s should be in peer noProxy", providerServiceName, cl.Name, cidr)
		}
	}

	clusterCIDRs, err := getClusterCIDR(ctx, cl)
	if err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("provider-cluster-cidr-%s", cl.Name), Status: StatusWarn,
			Message: fmt.Sprintf("Could not get cluster CIDR on %s: %v", cl.Name, err),
		})
	} else {
		noProxyHosts = append(noProxyHosts, clusterCIDRs...)

		for _, cidr := range clusterCIDRs {
			console.Step("%s on %s: cluster CIDR %s should be in peer noProxy", providerServiceName, cl.Name, cidr)
		}
	}

	return results, []providerEndpoint{
		{host: clusterIP, port: port, service: fmt.Sprintf("%s-clusterip", providerServiceName)},
	}, noProxyHosts
}

func isGlobalNetEnabled(ctx context.Context, cl *cluster.Cluster) (bool, *CheckResult) {
	dynClient, err := dynamic.NewForConfig(cl.RestConfig)
	if err != nil {
		return false, &CheckResult{
			Name: fmt.Sprintf("globalnet-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to create dynamic client on %s: %v", cl.Name, err),
		}
	}

	sub, err := dynClient.Resource(submarinerGVR).Namespace("submariner-operator").Get(
		ctx, "submariner", metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) || isNoMatchError(err) {
			console.Step("Submariner CR not found on %s — assuming GlobalNet is not enabled", cl.Name)
			return false, nil
		}

		return false, &CheckResult{
			Name: fmt.Sprintf("globalnet-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get Submariner CR on %s: %v", cl.Name, err),
		}
	}

	spec, _ := sub.Object["spec"].(map[string]interface{})
	if spec == nil {
		return false, nil
	}

	globalNet, _ := spec["globalCIDR"].(string)

	return globalNet != "", nil
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

func getClusterCIDR(ctx context.Context, cl *cluster.Cluster) ([]string, error) {
	var network configv1.Network

	err := cl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &network)
	if err != nil {
		return nil, fmt.Errorf("getting Network CR: %w", err)
	}

	var cidrs []string
	for _, cn := range network.Spec.ClusterNetwork {
		cidrs = append(cidrs, cn.CIDR)
	}

	if len(cidrs) == 0 {
		return nil, fmt.Errorf("no cluster networks found in Network CR spec")
	}

	return cidrs, nil
}

func checkServiceExport(ctx context.Context, cl *cluster.Cluster) CheckResult {
	dynClient, err := dynamic.NewForConfig(cl.RestConfig)
	if err != nil {
		return CheckResult{
			Name: fmt.Sprintf("service-export-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to create dynamic client on %s: %v", cl.Name, err),
		}
	}

	_, err = dynClient.Resource(serviceExportGVR).Namespace(storageNamespace).Get(
		ctx, providerServiceName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			console.Fail("ServiceExport for %s not found on %s", providerServiceName, cl.Name)

			return CheckResult{
				Name: fmt.Sprintf("service-export-%s", cl.Name), Status: StatusFail,
				Message: fmt.Sprintf("ServiceExport for %s not found in %s on %s", providerServiceName, storageNamespace, cl.Name),
			}
		}

		if isNoMatchError(err) {
			console.Fail("ServiceExport CRD not found on %s — multicluster services not configured", cl.Name)

			return CheckResult{
				Name: fmt.Sprintf("service-export-%s", cl.Name), Status: StatusFail,
				Message: fmt.Sprintf("ServiceExport CRD not available on %s — multicluster services API not installed", cl.Name),
			}
		}

		return CheckResult{
			Name: fmt.Sprintf("service-export-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to check ServiceExport on %s: %v", cl.Name, err),
		}
	}

	console.Pass("ServiceExport for %s found on %s", providerServiceName, cl.Name)

	return CheckResult{
		Name: fmt.Sprintf("service-export-%s", cl.Name), Status: StatusPass,
		Message: fmt.Sprintf("ServiceExport for %s exists in %s on %s", providerServiceName, storageNamespace, cl.Name),
	}
}

func discoverManagedClusterName(ctx context.Context, hub *cluster.Cluster, targetCl *cluster.Cluster) (string, error) {
	dynClient, err := dynamic.NewForConfig(hub.RestConfig)
	if err != nil {
		return "", fmt.Errorf("creating dynamic client for hub: %w", err)
	}

	list, err := dynClient.Resource(managedClusterGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing ManagedClusters on hub: %w", err)
	}

	targetHost := normalizeAPIURL(targetCl.RestConfig.Host)

	for _, mc := range list.Items {
		spec, ok := mc.Object["spec"].(map[string]interface{})
		if !ok {
			continue
		}

		configs, ok := spec["managedClusterClientConfigs"].([]interface{})
		if !ok {
			continue
		}

		for _, cfg := range configs {
			cfgMap, ok := cfg.(map[string]interface{})
			if !ok {
				continue
			}

			mcURL, _ := cfgMap["url"].(string)
			if mcURL != "" && normalizeAPIURL(mcURL) == targetHost {
				return mc.GetName(), nil
			}
		}
	}

	return "", fmt.Errorf("no ManagedCluster on hub matches API server %s for %s", targetHost, targetCl.Name)
}

func normalizeAPIURL(rawURL string) string {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimRight(rawURL, "/")
	}

	return strings.TrimRight(u.Host, "/")
}

func getExportedAddress(ctx context.Context, cl *cluster.Cluster) (string, error) {
	dynClient, err := dynamic.NewForConfig(cl.RestConfig)
	if err != nil {
		return "", fmt.Errorf("creating dynamic client: %w", err)
	}

	obj, err := dynClient.Resource(storageClusterGVR).Namespace(storageNamespace).Get(
		ctx, "ocs-storagecluster", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting StorageCluster: %w", err)
	}

	return obj.GetAnnotations()[exportedAddressAnnotation], nil
}

func parseExportedAddress(addr string) (string, int32, string) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, defaultProviderPort, classifyEndpointService(addr)
	}

	p, err := strconv.Atoi(portStr)
	if err != nil {
		return host, defaultProviderPort, classifyEndpointService(host)
	}

	return host, int32(p), classifyEndpointService(host)
}

func classifyEndpointService(host string) string {
	if strings.Contains(host, ".svc.clusterset.local") {
		return fmt.Sprintf("%s-submariner", providerServiceName)
	}

	return providerLBName
}

func checkProviderNoProxy(ctx context.Context, peerCl *cluster.Cluster, srcClName string, hosts []string) []CheckResult {
	var proxy configv1.Proxy

	err := peerCl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &proxy)
	if err != nil {
		if errors.IsNotFound(err) || isNoMatchError(err) {
			return nil
		}

		return []CheckResult{{
			Name: fmt.Sprintf("provider-proxy-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to get proxy config on %s: %v", peerCl.Name, err),
		}}
	}

	if proxy.Spec.HTTPProxy == "" && proxy.Spec.HTTPSProxy == "" {
		return nil
	}

	noProxyEntries := parseNoProxy(proxy.Spec.NoProxy)
	var results []CheckResult

	seen := map[string]bool{}

	for _, host := range hosts {
		if seen[host] {
			continue
		}

		seen[host] = true
		name := fmt.Sprintf("provider-noproxy-%s-%s-%s", peerCl.Name, srcClName, host)

		if isHostCoveredByNoProxy(host, noProxyEntries) {
			console.Pass("%s: provider endpoint %s (from %s) is in noProxy", peerCl.Name, host, srcClName)
			results = append(results, CheckResult{
				Name: name, Status: StatusPass,
				Message: fmt.Sprintf("%s: provider endpoint %s (from %s) is in noProxy", peerCl.Name, host, srcClName),
			})
		} else {
			console.Fail("%s: provider endpoint %s (from %s) is NOT in noProxy — add it via:\n"+
				"        oc edit proxy/cluster and add %s to spec.noProxy",
				peerCl.Name, host, srcClName, host)
			results = append(results, CheckResult{
				Name: name, Status: StatusFail,
				Message: fmt.Sprintf("%s: provider endpoint %s (from %s) is NOT in noProxy — "+
					"add it via: oc edit proxy/cluster and add %s to spec.noProxy",
					peerCl.Name, host, srcClName, host),
			})
		}
	}

	return results
}

func testProviderReachability(ctx context.Context, peerCl *cluster.Cluster, srcClName string, endpoints []providerEndpoint) []CheckResult {
	podName := fmt.Sprintf("dr-check-provider-%s", peerCl.Name)

	if _, err := DeployTestPod(ctx, peerCl.Clientset, podName, peerCl.Namespace, false, ""); err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("provider-reach-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to deploy test pod on %s: %v", peerCl.Name, err),
		}}
	}

	defer func() {
		_ = DeleteTestPod(context.Background(), peerCl.Clientset, podName, peerCl.Namespace)
	}()

	if err := WaitForPodReady(ctx, peerCl.Clientset, podName, peerCl.Namespace); err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("provider-reach-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Test pod on %s not ready: %v", peerCl.Name, err),
		}}
	}

	proxyEnv := getProxyEnv(ctx, peerCl)

	var results []CheckResult

	for _, ep := range endpoints {
		result := testProviderEndpoint(ctx, peerCl, podName, srcClName, ep, proxyEnv)
		results = append(results, result)

		if result.Status == StatusPass {
			console.Pass("%s", result.Message)
		} else {
			console.Fail("%s", result.Message)
		}
	}

	return results
}

func getProxyEnv(ctx context.Context, cl *cluster.Cluster) string {
	var proxy configv1.Proxy

	err := cl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &proxy)
	if err != nil {
		return ""
	}

	var parts []string

	if proxy.Status.HTTPProxy != "" {
		parts = append(parts, fmt.Sprintf("HTTP_PROXY=%s http_proxy=%s", proxy.Status.HTTPProxy, proxy.Status.HTTPProxy))
	}

	if proxy.Status.HTTPSProxy != "" {
		parts = append(parts, fmt.Sprintf("HTTPS_PROXY=%s https_proxy=%s", proxy.Status.HTTPSProxy, proxy.Status.HTTPSProxy))
	}

	if proxy.Status.NoProxy != "" {
		parts = append(parts, fmt.Sprintf("NO_PROXY=%s no_proxy=%s", proxy.Status.NoProxy, proxy.Status.NoProxy))
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, " ") + " "
}

func testProviderEndpoint(ctx context.Context, peerCl *cluster.Cluster, podName, srcClName string, ep providerEndpoint, proxyEnv string) CheckResult {
	name := fmt.Sprintf("provider-reach:%s->%s(%s:%d)", peerCl.Name, srcClName, ep.host, ep.port)

	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("%scurl -sk --max-time 5 https://%s:%d", proxyEnv, ep.host, ep.port),
	}

	_, _, err := ExecInPod(ctx, peerCl.RestConfig, peerCl.Clientset, podName, peerCl.Namespace, cmd)
	if err != nil {
		return CheckResult{
			Name:   name,
			Status: StatusFail,
			Message: fmt.Sprintf("%s endpoint %s:%d unreachable from %s — %s",
				ep.service, ep.host, ep.port, peerCl.Name, reachabilityHint(ep)),
		}
	}

	return CheckResult{
		Name:   name,
		Status: StatusPass,
		Message: fmt.Sprintf("%s endpoint %s:%d reachable from %s",
			ep.service, ep.host, ep.port, peerCl.Name),
	}
}

func reachabilityHint(ep providerEndpoint) string {
	switch {
	case strings.HasSuffix(ep.service, "-submariner"):
		return "check Submariner gateway status and connectivity between clusters"
	case strings.HasSuffix(ep.service, "-clusterip"):
		return "check Submariner routing and ensure cluster/service CIDRs are in noProxy"
	case strings.HasSuffix(ep.service, "-nodeport"):
		return "check firewall rules allow traffic to the NodePort and node is reachable"
	case ep.service == providerLBName:
		return "check LoadBalancer IP is allocated and firewall allows port " + fmt.Sprintf("%d", ep.port)
	default:
		return "check firewall rules and ensure the endpoint is in noProxy if proxy is configured"
	}
}

func getLoadBalancerHost(svc *corev1.Service) string {
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			return ingress.IP
		}

		if ingress.Hostname != "" {
			return ingress.Hostname
		}
	}

	return ""
}

func getServicePort(svc *corev1.Service) int32 {
	if len(svc.Spec.Ports) > 0 {
		return svc.Spec.Ports[0].Port
	}

	return 0
}

func getNodePort(svc *corev1.Service) int32 {
	if len(svc.Spec.Ports) > 0 {
		return svc.Spec.Ports[0].NodePort
	}

	return 0
}

func getServiceCIDR(ctx context.Context, cl *cluster.Cluster) ([]string, error) {
	var network configv1.Network

	err := cl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &network)
	if err != nil {
		return nil, fmt.Errorf("getting Network CR: %w", err)
	}

	if len(network.Status.ServiceNetwork) == 0 {
		return nil, fmt.Errorf("no service networks found in Network CR status")
	}

	return network.Status.ServiceNetwork, nil
}

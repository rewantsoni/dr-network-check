package check

import (
	"context"
	"fmt"
	"net"
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

var serviceExportGVR = schema.GroupVersionResource{
	Group:    "multicluster.x-k8s.io",
	Version:  "v1alpha1",
	Resource: "serviceexports",
}

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

		results = append(results, CheckEndpointNoProxy(peerCl.Proxy, peerCl.Name, cl.Name, "provider", []string{host})...)
		results = append(results, testProviderReachability(ctx, peerCl, cl.Name, endpoints)...)

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
		results = append(results, CheckEndpointNoProxy(peerCl.Proxy, peerCl.Name, cl.Name, "provider", noProxyHosts)...)
	}

	if len(endpoints) > 0 {
		results = append(results, testProviderReachability(ctx, peerCl, cl.Name, endpoints)...)
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

	port := getServicePort(svc)

	if cl.Submariner.GlobalNet {
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

	proxyEnv := peerCl.Proxy.Env
	grpcurlAvailable := installGrpcurl(ctx, peerCl, podName)

	var results []CheckResult

	for _, ep := range endpoints {
		curlResult := testProviderEndpoint(ctx, peerCl, podName, srcClName, ep, proxyEnv)
		results = append(results, curlResult)

		if curlResult.Status == StatusPass {
			console.Pass("%s", curlResult.Message)
		} else {
			console.Fail("%s", curlResult.Message)
		}

		if curlResult.Status == StatusPass && grpcurlAvailable {
			grpcResult := testProviderGrpc(ctx, peerCl, podName, srcClName, ep, proxyEnv)
			results = append(results, grpcResult)

			if grpcResult.Status == StatusPass {
				console.Pass("%s", grpcResult.Message)
			} else {
				console.Fail("%s", grpcResult.Message)
			}
		}
	}

	return results
}


func testProviderEndpoint(ctx context.Context, peerCl *cluster.Cluster, podName, srcClName string, ep providerEndpoint, proxyEnv string) CheckResult {
	name := fmt.Sprintf("provider-reach:%s->%s(%s:%d)", peerCl.Name, srcClName, ep.host, ep.port)

	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("%scurl -svk --max-time 5 https://%s:%d 2>&1", proxyEnv, ep.host, ep.port),
	}

	stdout, _, err := ExecInPod(ctx, peerCl.RestConfig, peerCl.Clientset, podName, peerCl.Namespace, cmd)
	if err != nil {
		detail := extractCurlError(stdout)
		dnsInfo := resolveDNS(ctx, peerCl, podName, ep.host)

		msg := fmt.Sprintf("%s endpoint %s:%d unreachable from %s\n"+
			"        curl error: %s\n"+
			"        dns lookup: %s\n"+
			"        hint: %s",
			ep.service, ep.host, ep.port, peerCl.Name,
			detail, dnsInfo, reachabilityHint(ep))

		return CheckResult{Name: name, Status: StatusFail, Message: msg}
	}

	return CheckResult{
		Name:   name,
		Status: StatusPass,
		Message: fmt.Sprintf("%s endpoint %s:%d reachable from %s",
			ep.service, ep.host, ep.port, peerCl.Name),
	}
}

func extractCurlError(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "curl:") {
			return trimmed
		}
	}

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "Could not resolve") ||
			strings.Contains(trimmed, "Connection refused") ||
			strings.Contains(trimmed, "Connection timed out") ||
			strings.Contains(trimmed, "No route to host") ||
			strings.Contains(trimmed, "Failed to connect") {
			return trimmed
		}
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" {
			return last
		}
	}

	return "unknown error"
}

func resolveDNS(ctx context.Context, cl *cluster.Cluster, podName, host string) string {
	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("getent hosts %s 2>&1 || echo 'resolution failed'", host),
	}

	stdout, _, err := ExecInPod(ctx, cl.RestConfig, cl.Clientset, podName, cl.Namespace, cmd)
	if err != nil {
		return fmt.Sprintf("failed to resolve %s", host)
	}

	return strings.TrimSpace(stdout)
}

const grpcurlVersion = "1.9.2"

func installGrpcurl(ctx context.Context, cl *cluster.Cluster, podName string) bool {
	cmd := []string{
		"bash", "-c",
		fmt.Sprintf(
			`ARCH=$(uname -m); case $ARCH in aarch64) ARCH=arm64;; esac; `+
				`curl -sL --max-time 30 https://github.com/fullstorydev/grpcurl/releases/download/v%s/grpcurl_%s_linux_${ARCH}.tar.gz `+
				`| tar xz -C /tmp grpcurl && /tmp/grpcurl --version`,
			grpcurlVersion, grpcurlVersion),
	}

	_, _, err := ExecInPod(ctx, cl.RestConfig, cl.Clientset, podName, cl.Namespace, cmd)
	if err != nil {
		console.Warn("Could not install grpcurl in test pod on %s — skipping gRPC checks", cl.Name)

		return false
	}

	console.Step("Installed grpcurl in test pod on %s", cl.Name)

	return true
}

func testProviderGrpc(ctx context.Context, peerCl *cluster.Cluster, podName, srcClName string, ep providerEndpoint, proxyEnv string) CheckResult {
	name := fmt.Sprintf("provider-grpc:%s->%s(%s:%d)", peerCl.Name, srcClName, ep.host, ep.port)

	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("%s/tmp/grpcurl -insecure -max-time 5 %s:%d list 2>&1", proxyEnv, ep.host, ep.port),
	}

	stdout, _, err := ExecInPod(ctx, peerCl.RestConfig, peerCl.Clientset, podName, peerCl.Namespace, cmd)
	if err != nil {
		if strings.Contains(stdout, "does not support the reflection API") {
			return CheckResult{
				Name: name, Status: StatusPass,
				Message: fmt.Sprintf("gRPC server at %s:%d responding from %s (reflection not enabled)",
					ep.host, ep.port, peerCl.Name),
			}
		}

		detail := strings.TrimSpace(stdout)
		if detail == "" {
			detail = err.Error()
		}

		return CheckResult{
			Name: name, Status: StatusFail,
			Message: fmt.Sprintf("gRPC server at %s:%d not responding from %s\n"+
				"        grpcurl error: %s\n"+
				"        hint: %s",
				ep.host, ep.port, peerCl.Name, detail, reachabilityHint(ep)),
		}
	}

	if strings.Contains(stdout, "provider.OCSProvider") {
		return CheckResult{
			Name: name, Status: StatusPass,
			Message: fmt.Sprintf("gRPC service provider.OCSProvider available at %s:%d from %s",
				ep.host, ep.port, peerCl.Name),
		}
	}

	return CheckResult{
		Name: name, Status: StatusPass,
		Message: fmt.Sprintf("gRPC server at %s:%d responding from %s",
			ep.host, ep.port, peerCl.Name),
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

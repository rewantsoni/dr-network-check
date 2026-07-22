package check

import (
	"context"
	"fmt"

	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

func checkCephSubmarinerClusterIP(ctx context.Context, cl, peerCl *cluster.Cluster) []CheckResult {
	endpoints, err := discoverDaemonEndpoints(ctx, cl)
	if err != nil {
		console.Fail("Failed to discover daemon endpoints on %s: %v", cl.Name, err)

		return []CheckResult{{
			Name: fmt.Sprintf("ceph-discover-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover daemon endpoints on %s: %v", cl.Name, err),
		}}
	}

	logEndpoints(cl.Name, endpoints)

	podName := fmt.Sprintf("dr-check-ceph-%s", peerCl.Name)

	if _, err := DeployTestPod(ctx, peerCl.Clientset, podName, peerCl.Namespace, false, ""); err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("ceph-reach-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to deploy test pod on %s: %v", peerCl.Name, err),
		}}
	}

	defer func() {
		_ = DeleteTestPod(context.Background(), peerCl.Clientset, podName, peerCl.Namespace)
	}()

	if err := WaitForPodReady(ctx, peerCl.Clientset, podName, peerCl.Namespace); err != nil {
		return []CheckResult{{
			Name: fmt.Sprintf("ceph-reach-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Test pod on %s not ready: %v", peerCl.Name, err),
		}}
	}

	var results []CheckResult

	for _, ep := range endpoints {
		result := testDaemonTCP(ctx, peerCl, podName,
			ep.nodeIP, ep.port,
			fmt.Sprintf("ceph-reach-%s-%s-%s", ep.daemon, peerCl.Name, cl.Name),
			fmt.Sprintf("%s port %d: %s -> %s (%s)", ep.daemon, ep.port, peerCl.Name, ep.nodeIP, cl.Name),
		)
		results = append(results, result)
		printResult(result)
	}

	return results
}

func checkCephSubmarinerGlobalNet(ctx context.Context, cl, peerCl *cluster.Cluster, managedClusterName string) []CheckResult {
	endpoints, err := discoverDaemonEndpoints(ctx, cl)
	if err != nil {
		console.Fail("Failed to discover daemon endpoints on %s: %v", cl.Name, err)

		return []CheckResult{{
			Name: fmt.Sprintf("ceph-mcs-discover-%s", cl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover daemon endpoints on %s: %v", cl.Name, err),
		}}
	}

	logEndpoints(cl.Name, endpoints)

	type resolvedEndpoint struct {
		daemon  string
		ip      string
		port    int32
		svcName string
	}

	var resolved []resolvedEndpoint
	var noProxyIPs []string
	var results []CheckResult

	for _, ep := range endpoints {
		svcRef := "rook-ceph-" + ep.daemon

		subSvc, err := findSubmarinerService(ctx, cl, svcRef)
		if err != nil {
			console.Fail("%s: Submariner service not found for %s on %s — %v",
				ep.daemon, svcRef, cl.Name, err)
			results = append(results, CheckResult{
				Name:   fmt.Sprintf("ceph-mcs-%s-%s-%s", ep.daemon, peerCl.Name, cl.Name),
				Status: StatusFail,
				Message: fmt.Sprintf("%s: Submariner service not found for %s on %s — %v",
					ep.daemon, svcRef, cl.Name, err),
			})

			continue
		}

		ip, port, err := submarinerServiceEndpoint(subSvc)
		if err != nil {
			console.Fail("%s: invalid Submariner service %s on %s — %v",
				ep.daemon, subSvc.Name, cl.Name, err)
			results = append(results, CheckResult{
				Name:   fmt.Sprintf("ceph-mcs-%s-%s-%s", ep.daemon, peerCl.Name, cl.Name),
				Status: StatusFail,
				Message: fmt.Sprintf("%s: invalid Submariner service %s on %s — %v",
					ep.daemon, subSvc.Name, cl.Name, err),
			})

			continue
		}

		console.Step("%s: Submariner service %s on %s — %s:%d", ep.daemon, subSvc.Name, cl.Name, ip, port)
		noProxyIPs = append(noProxyIPs, ip)
		resolved = append(resolved, resolvedEndpoint{daemon: ep.daemon, ip: ip, port: port, svcName: subSvc.Name})
	}

	results = append(results, CheckEndpointNoProxy(peerCl.Proxy, peerCl.Name, cl.Name, "ceph", noProxyIPs)...)

	if len(resolved) == 0 {
		return results
	}

	podName := fmt.Sprintf("dr-check-ceph-mcs-%s", peerCl.Name)

	if _, err := DeployTestPod(ctx, peerCl.Clientset, podName, peerCl.Namespace, false, ""); err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("ceph-mcs-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Failed to deploy test pod on %s: %v", peerCl.Name, err),
		})

		return results
	}

	defer func() {
		_ = DeleteTestPod(context.Background(), peerCl.Clientset, podName, peerCl.Namespace)
	}()

	if err := WaitForPodReady(ctx, peerCl.Clientset, podName, peerCl.Namespace); err != nil {
		results = append(results, CheckResult{
			Name: fmt.Sprintf("ceph-mcs-pod-%s", peerCl.Name), Status: StatusFail,
			Message: fmt.Sprintf("Test pod on %s not ready: %v", peerCl.Name, err),
		})

		return results
	}

	for _, ep := range resolved {
		result := testDaemonTCP(ctx, peerCl, podName,
			ep.ip, ep.port,
			fmt.Sprintf("ceph-mcs-%s-%s-%s", ep.daemon, peerCl.Name, cl.Name),
			fmt.Sprintf("%s port %d: %s -> %s (submariner-svc: %s)", ep.daemon, ep.port, peerCl.Name, ep.ip, ep.svcName),
		)
		results = append(results, result)
		printResult(result)
	}

	return results
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

func findSubmarinerService(ctx context.Context, cl *cluster.Cluster, exportedServiceRef string) (*corev1.Service, error) {
	svcs, err := cl.Clientset.CoreV1().Services(storageNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("submariner.io/exportedServiceRef=%s", exportedServiceRef),
	})
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}

	if len(svcs.Items) == 0 {
		return nil, fmt.Errorf("no Submariner service with label submariner.io/exportedServiceRef=%s in %s",
			exportedServiceRef, storageNamespace)
	}

	return &svcs.Items[0], nil
}

func submarinerServiceEndpoint(svc *corev1.Service) (string, int32, error) {
	if len(svc.Spec.ExternalIPs) == 0 {
		return "", 0, fmt.Errorf("no externalIPs on service %s", svc.Name)
	}

	if len(svc.Spec.Ports) == 0 {
		return "", 0, fmt.Errorf("no ports on service %s", svc.Name)
	}

	return svc.Spec.ExternalIPs[0], svc.Spec.Ports[0].Port, nil
}

package check

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const storageNamespace = "openshift-storage"

type S3Route struct {
	Cluster string
	Name    string
	Host    string
	URL     string
}

func DiscoverS3Routes(ctx context.Context, cl *cluster.Cluster) ([]S3Route, error) {
	var routeList routev1.RouteList
	if err := cl.Client.List(ctx, &routeList,
		ctrlclient.InNamespace(storageNamespace),
	); err != nil {
		return nil, fmt.Errorf("listing routes in %s on %s: %w", storageNamespace, cl.Name, err)
	}

	var routes []S3Route

	for i := range routeList.Items {
		route := &routeList.Items[i]

		if route.Name != "s3" {
			continue
		}

		host := getAdmittedHost(route)
		if host == "" {
			continue
		}

		routes = append(routes, S3Route{
			Cluster: cl.Name,
			Name:    route.Name,
			Host:    host,
			URL:     "https://" + host,
		})
	}

	return routes, nil
}

func getAdmittedHost(route *routev1.Route) string {
	for _, ingress := range route.Status.Ingress {
		for _, cond := range ingress.Conditions {
			if cond.Type == routev1.RouteAdmitted && cond.Status == "True" {
				return ingress.Host
			}
		}
	}

	if len(route.Status.Ingress) > 0 {
		return route.Status.Ingress[0].Host
	}

	return route.Spec.Host
}

func CheckS3Routes(ctx context.Context, clusters *cluster.Clusters) ([]S3Route, []CheckResult) {
	console.Info("Checking S3 route reachability")

	routesC1, err := DiscoverS3Routes(ctx, clusters.C1)
	if err != nil {
		return nil, []CheckResult{{
			Name: "discover-s3-c1", Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover S3 routes on %s: %v", clusters.C1.Name, err),
		}}
	}

	routesC2, err := DiscoverS3Routes(ctx, clusters.C2)
	if err != nil {
		return nil, []CheckResult{{
			Name: "discover-s3-c2", Status: StatusFail,
			Message: fmt.Sprintf("Failed to discover S3 routes on %s: %v", clusters.C2.Name, err),
		}}
	}

	allRoutes := append(routesC1, routesC2...)
	if len(allRoutes) == 0 {
		console.Warn("No S3 routes found on either ODF cluster")

		return nil, []CheckResult{{
			Name: "discover-s3", Status: StatusWarn,
			Message: "No S3 routes found in openshift-storage namespace on ODF clusters",
		}}
	}

	for _, r := range allRoutes {
		console.Step("Discovered S3 route on %s: %s", r.Cluster, r.Host)
	}

	type checkTarget struct {
		cluster *cluster.Cluster
		label   string
	}

	seen := map[string]bool{}
	var targets []checkTarget

	for _, t := range []checkTarget{
		{cluster: clusters.Hub, label: clusters.Hub.Name},
		{cluster: clusters.C1Client1, label: clusters.C1Client1.Name},
		{cluster: clusters.C2Client1, label: clusters.C2Client1.Name},
	} {
		if !seen[t.cluster.Kubeconfig] {
			seen[t.cluster.Kubeconfig] = true
			targets = append(targets, t)
		}
	}

	if clusters.HubPassive != nil && !seen[clusters.HubPassive.Kubeconfig] {
		seen[clusters.HubPassive.Kubeconfig] = true
		targets = append(targets, checkTarget{cluster: clusters.HubPassive, label: clusters.HubPassive.Name})
	}

	var results []CheckResult

	podCount := 0

	for _, target := range targets {
		proxyResults := checkS3ProxyConfig(ctx, target.cluster, target.label, allRoutes)
		results = append(results, proxyResults...)

		podName := fmt.Sprintf("dr-check-s3-test-%d", podCount)
		podCount++

		if _, err := DeployTestPod(ctx, target.cluster.Clientset, podName, target.cluster.Namespace, false, ""); err != nil {
			results = append(results, CheckResult{
				Name: fmt.Sprintf("deploy-s3-pod-%s", target.label), Status: StatusFail,
				Message: fmt.Sprintf("Failed to deploy test pod on %s: %v", target.label, err),
			})

			continue
		}

		defer func() {
			_ = DeleteTestPod(context.Background(), target.cluster.Clientset, podName, target.cluster.Namespace)
		}()

		if err := WaitForPodReady(ctx, target.cluster.Clientset, podName, target.cluster.Namespace); err != nil {
			results = append(results, CheckResult{
				Name: fmt.Sprintf("wait-s3-pod-%s", target.label), Status: StatusFail,
				Message: fmt.Sprintf("Test pod on %s not ready: %v", target.label, err),
			})

			continue
		}

		proxyEnv := getProxyEnv(ctx, target.cluster)

		for _, route := range allRoutes {
			result := testS3Reachability(ctx, target.cluster, podName, target.label, route, proxyEnv)
			results = append(results, result)

			if result.Status == StatusPass {
				console.Pass("%s", result.Message)
			} else {
				console.Fail("%s", result.Message)
			}
		}
	}

	return allRoutes, results
}

func checkS3ProxyConfig(ctx context.Context, cl *cluster.Cluster, label string, routes []S3Route) []CheckResult {
	var proxy configv1.Proxy

	err := cl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &proxy)
	if err != nil {
		if errors.IsNotFound(err) || isNoMatchError(err) {
			return nil
		}

		return []CheckResult{{
			Name: fmt.Sprintf("s3-proxy-%s", label), Status: StatusFail,
			Message: fmt.Sprintf("%s: failed to get proxy config: %v", label, err),
		}}
	}

	if proxy.Spec.HTTPProxy == "" && proxy.Spec.HTTPSProxy == "" {
		return nil
	}

	noProxyEntries := parseNoProxy(proxy.Spec.NoProxy)
	var results []CheckResult

	for _, route := range routes {
		name := fmt.Sprintf("s3-noproxy-%s-%s", label, route.Host)

		if isHostCoveredByNoProxy(route.Host, noProxyEntries) {
			console.Pass("%s: S3 endpoint %s is in noProxy", label, route.Host)
			results = append(results, CheckResult{
				Name: name, Status: StatusPass,
				Message: fmt.Sprintf("%s: S3 endpoint %s is in noProxy", label, route.Host),
			})
		} else {
			console.Fail("%s: S3 endpoint %s is NOT in noProxy (proxy: %s) — add it via:\n"+
				"        oc edit proxy/cluster on %s and add %s to spec.noProxy",
				label, route.Host, proxyURL(&proxy), label, route.Host)
			results = append(results, CheckResult{
				Name: name, Status: StatusFail,
				Message: fmt.Sprintf("%s: S3 endpoint %s is NOT in noProxy — "+
					"add it via: oc edit proxy/cluster on %s and add %s to spec.noProxy",
					label, route.Host, label, route.Host),
			})
		}
	}

	return results
}

func testS3Reachability(ctx context.Context, cl *cluster.Cluster, podName, label string,
	route S3Route, proxyEnv string,
) CheckResult {
	name := fmt.Sprintf("s3-reach:%s->%s(%s)", label, route.Cluster, route.Host)

	cmd := []string{
		"bash", "-c",
		fmt.Sprintf("%scurl -sk -o /dev/null -w '%%{http_code}' --connect-timeout 10 %s", proxyEnv, route.URL),
	}

	stdout, _, err := ExecInPod(ctx, cl.RestConfig, cl.Clientset, podName, cl.Namespace, cmd)
	if err != nil {
		return CheckResult{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("S3 route %s (%s) unreachable from %s: %v", route.Host, route.Cluster, label, err),
		}
	}

	if stdout == "000" {
		return CheckResult{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("S3 route %s (%s) unreachable from %s: connection failed", route.Host, route.Cluster, label),
		}
	}

	return CheckResult{
		Name:    name,
		Status:  StatusPass,
		Message: fmt.Sprintf("S3 route %s (%s) reachable from %s (HTTP %s)", route.Host, route.Cluster, label, stdout),
	}
}

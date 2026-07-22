package check

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
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

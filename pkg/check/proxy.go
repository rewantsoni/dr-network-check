package check

import (
	"context"
	"fmt"
	"net"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func FetchProxyConfig(ctx context.Context, cl *cluster.Cluster) error {
	var proxy configv1.Proxy

	err := cl.Client.Get(ctx, types.NamespacedName{Name: "cluster"}, &proxy)
	if err != nil {
		if errors.IsNotFound(err) || isNoMatchError(err) {
			return nil
		}

		return fmt.Errorf("getting proxy config on %s: %w", cl.Name, err)
	}

	cl.Proxy = cluster.ProxyConfig{
		Configured: proxy.Spec.HTTPProxy != "" || proxy.Spec.HTTPSProxy != "",
		ProxyURL:   proxyURL(&proxy),
		Env:        buildProxyEnv(&proxy),
	}

	if cl.Proxy.Configured {
		cl.Proxy.NoProxy = parseNoProxy(proxy.Spec.NoProxy)
	}

	return nil
}

func CheckEndpointNoProxy(proxy cluster.ProxyConfig, clName, srcClName, endpointType string, hosts []string) []CheckResult {
	if !proxy.Configured {
		return nil
	}

	var results []CheckResult

	seen := map[string]bool{}

	for _, host := range hosts {
		if seen[host] {
			continue
		}

		seen[host] = true

		var nameKey, desc string

		if srcClName != "" {
			nameKey = fmt.Sprintf("%s-noproxy-%s-%s-%s", endpointType, clName, srcClName, host)
			desc = fmt.Sprintf("%s endpoint %s (from %s)", endpointType, host, srcClName)
		} else {
			nameKey = fmt.Sprintf("%s-noproxy-%s-%s", endpointType, clName, host)
			desc = fmt.Sprintf("%s endpoint %s", endpointType, host)
		}

		if isHostCoveredByNoProxy(host, proxy.NoProxy) {
			console.Pass("%s: %s is in noProxy", clName, desc)
			results = append(results, CheckResult{
				Name: nameKey, Status: StatusPass,
				Message: fmt.Sprintf("%s: %s is in noProxy", clName, desc),
			})
		} else {
			console.Fail("%s: %s is NOT in noProxy — add it via:\n"+
				"        oc edit proxy/cluster and add %s to spec.noProxy",
				clName, desc, host)
			results = append(results, CheckResult{
				Name: nameKey, Status: StatusFail,
				Message: fmt.Sprintf("%s: %s is NOT in noProxy — "+
					"add it via: oc edit proxy/cluster and add %s to spec.noProxy",
					clName, desc, host),
			})
		}
	}

	return results
}

func buildProxyEnv(proxy *configv1.Proxy) string {
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

func parseNoProxy(noProxy string) []string {
	if noProxy == "" {
		return nil
	}

	var entries []string

	for _, entry := range strings.Split(noProxy, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			entries = append(entries, entry)
		}
	}

	return entries
}

func isHostCoveredByNoProxy(host string, noProxyEntries []string) bool {
	host = strings.ToLower(host)
	hostIP := net.ParseIP(host)

	for _, entry := range noProxyEntries {
		entry = strings.ToLower(strings.TrimSpace(entry))

		if entry == host {
			return true
		}

		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil {
				if hostIP != nil && cidr.Contains(hostIP) {
					return true
				}

				if strings.Contains(host, "/") {
					_, hostCIDR, err := net.ParseCIDR(host)
					if err == nil && cidrContains(cidr, hostCIDR) {
						return true
					}
				}
			}

			continue
		}

		if strings.HasPrefix(entry, ".") && strings.HasSuffix(host, entry) {
			return true
		}

		if !strings.HasPrefix(entry, ".") && strings.HasSuffix(host, "."+entry) {
			return true
		}
	}

	return false
}

func cidrContains(outer, inner *net.IPNet) bool {
	onesOuter, _ := outer.Mask.Size()
	onesInner, _ := inner.Mask.Size()

	return outer.Contains(inner.IP) && onesOuter <= onesInner
}

func proxyURL(proxy *configv1.Proxy) string {
	if proxy.Spec.HTTPSProxy != "" {
		return proxy.Spec.HTTPSProxy
	}

	return proxy.Spec.HTTPProxy
}

func isNoMatchError(err error) bool {
	return strings.Contains(err.Error(), "no matches for kind")
}

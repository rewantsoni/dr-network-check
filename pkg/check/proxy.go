package check

import (
	"net"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
)

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

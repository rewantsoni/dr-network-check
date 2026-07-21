package check

import (
	"errors"
	"net"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestParseNoProxy(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"single", "localhost", []string{"localhost"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"with spaces", " a , b , c ", []string{"a", "b", "c"}},
		{"trailing comma", "a,b,", []string{"a", "b"}},
		{"leading comma", ",a,b", []string{"a", "b"}},
		{"only commas", ",,,", nil},
		{"cidr entry", "10.0.0.0/16,172.16.0.0/12", []string{"10.0.0.0/16", "172.16.0.0/12"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNoProxy(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseNoProxy(%q) = %v, want %v", tt.input, got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseNoProxy(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsHostCoveredByNoProxy(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		entries []string
		want    bool
	}{
		{"exact ip match", "10.0.0.1", []string{"10.0.0.1"}, true},
		{"exact hostname match", "api.cluster.local", []string{"api.cluster.local"}, true},
		{"no match", "10.0.0.1", []string{"10.0.0.2"}, false},
		{"empty entries", "10.0.0.1", nil, false},
		{"cidr covers ip", "10.48.101.50", []string{"10.48.0.0/16"}, true},
		{"cidr does not cover ip", "192.168.1.1", []string{"10.48.0.0/16"}, false},
		{"domain suffix with dot", "foo.example.com", []string{".example.com"}, true},
		{"domain suffix without dot", "foo.example.com", []string{"example.com"}, true},
		{"domain suffix no match", "foo.example.com", []string{"other.com"}, false},
		{"domain exact is not suffix", "example.com", []string{".example.com"}, false},
		{"case insensitive", "API.Cluster.Local", []string{"api.cluster.local"}, true},
		{"case insensitive entry", "api.cluster.local", []string{"API.Cluster.Local"}, true},
		{"cidr in cidr", "10.48.0.0/24", []string{"10.48.0.0/16"}, true},
		{"narrow cidr does not contain wide", "10.48.0.0/16", []string{"10.48.0.0/24"}, false},
		{"multiple entries one matches", "10.48.101.50", []string{"192.168.0.0/16", "10.48.0.0/16"}, true},
		{"subdomain match", "deep.sub.example.com", []string{"example.com"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHostCoveredByNoProxy(tt.host, tt.entries)
			if got != tt.want {
				t.Errorf("isHostCoveredByNoProxy(%q, %v) = %v, want %v", tt.host, tt.entries, got, tt.want)
			}
		})
	}
}

func TestCidrContains(t *testing.T) {
	tests := []struct {
		name  string
		outer string
		inner string
		want  bool
	}{
		{"outer contains inner", "10.0.0.0/16", "10.0.1.0/24", true},
		{"same cidr", "10.0.0.0/24", "10.0.0.0/24", true},
		{"inner wider than outer", "10.0.0.0/24", "10.0.0.0/16", false},
		{"non overlapping", "10.0.0.0/24", "192.168.0.0/24", false},
		{"large outer small inner", "10.0.0.0/8", "10.1.2.0/24", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outer, _ := net.ParseCIDR(tt.outer)
			_, inner, _ := net.ParseCIDR(tt.inner)
			got := cidrContains(outer, inner)
			if got != tt.want {
				t.Errorf("cidrContains(%q, %q) = %v, want %v", tt.outer, tt.inner, got, tt.want)
			}
		})
	}
}

func TestProxyURL(t *testing.T) {
	tests := []struct {
		name  string
		proxy configv1.Proxy
		want  string
	}{
		{
			"both set prefers https",
			configv1.Proxy{Spec: configv1.ProxySpec{HTTPProxy: "http://proxy:8080", HTTPSProxy: "https://proxy:8443"}},
			"https://proxy:8443",
		},
		{
			"only http",
			configv1.Proxy{Spec: configv1.ProxySpec{HTTPProxy: "http://proxy:8080"}},
			"http://proxy:8080",
		},
		{
			"only https",
			configv1.Proxy{Spec: configv1.ProxySpec{HTTPSProxy: "https://proxy:8443"}},
			"https://proxy:8443",
		},
		{
			"neither set",
			configv1.Proxy{},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proxyURL(&tt.proxy)
			if got != tt.want {
				t.Errorf("proxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsNoMatchError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"matching error", errors.New("no matches for kind \"Proxy\" in version \"config.openshift.io/v1\""), true},
		{"non matching error", errors.New("connection refused"), false},
		{"partial match", errors.New("there are no matches for kind"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNoMatchError(tt.err)
			if got != tt.want {
				t.Errorf("isNoMatchError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

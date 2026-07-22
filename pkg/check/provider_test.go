package check

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNormalizeAPIURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"full https url", "https://api.cluster.example.com:6443", "api.cluster.example.com:6443"},
		{"no scheme", "api.cluster.example.com:6443", "api.cluster.example.com:6443"},
		{"trailing slash", "https://api.cluster.example.com:6443/", "api.cluster.example.com:6443"},
		{"with path", "https://api.cluster.example.com:6443/api/v1", "api.cluster.example.com:6443"},
		{"http scheme", "http://api.cluster.example.com:6443", "api.cluster.example.com:6443"},
		{"no port", "https://api.cluster.example.com", "api.cluster.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeAPIURL(tt.input)
			if got != tt.want {
				t.Errorf("normalizeAPIURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetLoadBalancerHost(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
		want string
	}{
		{
			"ingress with ip",
			&corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}},
					},
				},
			},
			"10.0.0.1",
		},
		{
			"ingress with hostname",
			&corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}},
					},
				},
			},
			"lb.example.com",
		},
		{
			"ip preferred over hostname",
			&corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1", Hostname: "lb.example.com"}},
					},
				},
			},
			"10.0.0.1",
		},
		{
			"no ingress",
			&corev1.Service{},
			"",
		},
		{
			"empty ingress list",
			&corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{},
					},
				},
			},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getLoadBalancerHost(tt.svc)
			if got != tt.want {
				t.Errorf("getLoadBalancerHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetServicePort(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
		want int32
	}{
		{
			"has port",
			&corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 50051}},
				},
			},
			50051,
		},
		{
			"multiple ports returns first",
			&corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{Port: 50051}, {Port: 8080}},
				},
			},
			50051,
		},
		{
			"no ports",
			&corev1.Service{},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getServicePort(tt.svc)
			if got != tt.want {
				t.Errorf("getServicePort() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetNodePort(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
		want int32
	}{
		{
			"has nodeport",
			&corev1.Service{
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{{NodePort: 31659}},
				},
			},
			31659,
		},
		{
			"no ports",
			&corev1.Service{},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNodePort(tt.svc)
			if got != tt.want {
				t.Errorf("getNodePort() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseExportedAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		host    string
		port    int32
		service string
	}{
		{
			"lb ip with port",
			"10.48.101.50:50051",
			"10.48.101.50", 50051, providerLBName,
		},
		{
			"lb ip without port",
			"10.48.101.50",
			"10.48.101.50", 50051, providerLBName,
		},
		{
			"lb hostname with port",
			"lb.cloud.example.com:50051",
			"lb.cloud.example.com", 50051, providerLBName,
		},
		{
			"submariner dns with port",
			"f36l016.ocs-provider-server.openshift-storage.svc.clusterset.local:50051",
			"f36l016.ocs-provider-server.openshift-storage.svc.clusterset.local", 50051, "ocs-provider-server-submariner",
		},
		{
			"submariner dns without port",
			"f36l016.ocs-provider-server.openshift-storage.svc.clusterset.local",
			"f36l016.ocs-provider-server.openshift-storage.svc.clusterset.local", 50051, "ocs-provider-server-submariner",
		},
		{
			"custom port",
			"10.48.101.50:9443",
			"10.48.101.50", 9443, providerLBName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, svc := parseExportedAddress(tt.addr)
			if host != tt.host {
				t.Errorf("host = %q, want %q", host, tt.host)
			}
			if port != tt.port {
				t.Errorf("port = %d, want %d", port, tt.port)
			}
			if svc != tt.service {
				t.Errorf("service = %q, want %q", svc, tt.service)
			}
		})
	}
}

func TestClassifyEndpointService(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{"submariner dns", "f36l016.ocs-provider-server.openshift-storage.svc.clusterset.local", "ocs-provider-server-submariner"},
		{"ip address", "10.48.101.50", providerLBName},
		{"lb hostname", "lb.cloud.example.com", providerLBName},
		{"generic hostname", "some-host", providerLBName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyEndpointService(tt.host)
			if got != tt.want {
				t.Errorf("classifyEndpointService(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestExtractCurlError(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			"curl error line",
			"* Trying 10.0.0.1:50051...\n* connect to 10.0.0.1 port 50051 failed\ncurl: (7) Failed to connect to 10.0.0.1 port 50051: Connection refused",
			"curl: (7) Failed to connect to 10.0.0.1 port 50051: Connection refused",
		},
		{
			"could not resolve",
			"* Could not resolve host: foo.svc.clusterset.local\ncurl: (6) Could not resolve host: foo.svc.clusterset.local",
			"curl: (6) Could not resolve host: foo.svc.clusterset.local",
		},
		{
			"connection timed out",
			"* Trying 10.0.0.1:50051...\n* Connection timed out after 5000 milliseconds",
			"* Connection timed out after 5000 milliseconds",
		},
		{
			"no route to host",
			"* Trying 10.0.0.1:50051...\n* No route to host",
			"* No route to host",
		},
		{
			"empty output",
			"",
			"unknown error",
		},
		{
			"falls back to last line",
			"line1\nsome unexpected error",
			"some unexpected error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCurlError(tt.output)
			if got != tt.want {
				t.Errorf("extractCurlError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReachabilityHint(t *testing.T) {
	tests := []struct {
		name    string
		ep      providerEndpoint
		contains string
	}{
		{
			"submariner endpoint",
			providerEndpoint{service: "ocs-provider-server-submariner", port: 50051},
			"Submariner gateway",
		},
		{
			"clusterip endpoint",
			providerEndpoint{service: "ocs-provider-server-clusterip", port: 50051},
			"Submariner routing",
		},
		{
			"nodeport endpoint",
			providerEndpoint{service: "ocs-provider-server-nodeport", port: 31659},
			"NodePort",
		},
		{
			"load balancer service",
			providerEndpoint{service: providerLBName, port: 50051},
			"LoadBalancer",
		},
		{
			"generic provider service",
			providerEndpoint{service: "ocs-provider-server", port: 50051},
			"firewall",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reachabilityHint(tt.ep)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("reachabilityHint() = %q, want it to contain %q", got, tt.contains)
			}
		})
	}
}

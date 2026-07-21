package check

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantIP  string
		wantPort int32
		wantErr bool
	}{
		{"standard address", "10.0.0.1:6789", "10.0.0.1", 6789, false},
		{"with nonce", "10.0.0.1:6789/12345", "10.0.0.1", 6789, false},
		{"high port", "10.0.0.1:3300", "10.0.0.1", 3300, false},
		{"no colon", "10.0.0.1", "", 0, true},
		{"invalid port", "10.0.0.1:abc", "", 0, true},
		{"empty string", "", "", 0, true},
		{"with nonce and slash", "10.0.0.1:6789/0", "10.0.0.1", 6789, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, port, err := parseAddress(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAddress(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}

			if err != nil {
				return
			}

			if ip != tt.wantIP {
				t.Errorf("parseAddress(%q) ip = %q, want %q", tt.addr, ip, tt.wantIP)
			}

			if port != tt.wantPort {
				t.Errorf("parseAddress(%q) port = %d, want %d", tt.addr, port, tt.wantPort)
			}
		})
	}
}

func TestResolveMonNodes(t *testing.T) {
	tests := []struct {
		name string
		json string
		want map[string]string
	}{
		{
			"valid mapping",
			`{"node":{"a":{"Name":"node1","Address":"10.0.0.1"},"b":{"Name":"node2","Address":"10.0.0.2"}}}`,
			map[string]string{"a": "node1", "b": "node2"},
		},
		{
			"empty string",
			"",
			map[string]string{},
		},
		{
			"invalid json",
			"not json",
			map[string]string{},
		},
		{
			"empty node map",
			`{"node":{}}`,
			map[string]string{},
		},
		{
			"single mon",
			`{"node":{"c":{"Name":"worker-0","Address":"10.0.0.3"}}}`,
			map[string]string{"c": "worker-0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMonNodes(tt.json)
			if len(got) != len(tt.want) {
				t.Fatalf("resolveMonNodes() = %v, want %v", got, tt.want)
			}

			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("resolveMonNodes()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestGetNodeInternalIP(t *testing.T) {
	tests := []struct {
		name string
		node *corev1.Node
		want string
	}{
		{
			"has internal ip",
			&corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
					},
				},
			},
			"10.0.0.1",
		},
		{
			"multiple addresses returns internal",
			&corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeHostName, Address: "worker-0"},
						{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
						{Type: corev1.NodeExternalIP, Address: "203.0.113.1"},
					},
				},
			},
			"10.0.0.1",
		},
		{
			"no internal ip",
			&corev1.Node{
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{
						{Type: corev1.NodeHostName, Address: "worker-0"},
					},
				},
			},
			"",
		},
		{
			"no addresses",
			&corev1.Node{},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNodeInternalIP(tt.node)
			if got != tt.want {
				t.Errorf("getNodeInternalIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

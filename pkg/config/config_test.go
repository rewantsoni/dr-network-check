package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			"all required fields set",
			Config{
				Clusters: ClusterConfig{
					Hub: ClusterRef{Kubeconfig: "/path/to/hub"},
					C1:  ClusterRef{Kubeconfig: "/path/to/c1"},
					C2:  ClusterRef{Kubeconfig: "/path/to/c2"},
				},
			},
			false,
		},
		{
			"missing hub",
			Config{
				Clusters: ClusterConfig{
					C1: ClusterRef{Kubeconfig: "/path/to/c1"},
					C2: ClusterRef{Kubeconfig: "/path/to/c2"},
				},
			},
			true,
		},
		{
			"missing c1",
			Config{
				Clusters: ClusterConfig{
					Hub: ClusterRef{Kubeconfig: "/path/to/hub"},
					C2:  ClusterRef{Kubeconfig: "/path/to/c2"},
				},
			},
			true,
		},
		{
			"missing c2",
			Config{
				Clusters: ClusterConfig{
					Hub: ClusterRef{Kubeconfig: "/path/to/hub"},
					C1:  ClusterRef{Kubeconfig: "/path/to/c1"},
				},
			},
			true,
		},
		{
			"optional fields empty is valid",
			Config{
				Clusters: ClusterConfig{
					Hub: ClusterRef{Kubeconfig: "/path/to/hub"},
					C1:  ClusterRef{Kubeconfig: "/path/to/c1"},
					C2:  ClusterRef{Kubeconfig: "/path/to/c2"},
				},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEffectiveC1Client(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			"c1-client-1 set",
			Config{
				Clusters: ClusterConfig{
					C1:        ClusterRef{Kubeconfig: "/path/to/c1"},
					C1Client1: ClusterRef{Kubeconfig: "/path/to/c1-client"},
				},
			},
			"/path/to/c1-client",
		},
		{
			"c1-client-1 empty falls back to c1",
			Config{
				Clusters: ClusterConfig{
					C1: ClusterRef{Kubeconfig: "/path/to/c1"},
				},
			},
			"/path/to/c1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.EffectiveC1Client()
			if got.Kubeconfig != tt.want {
				t.Errorf("EffectiveC1Client().Kubeconfig = %q, want %q", got.Kubeconfig, tt.want)
			}
		})
	}
}

func TestEffectiveC2Client(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			"c2-client-1 set",
			Config{
				Clusters: ClusterConfig{
					C2:        ClusterRef{Kubeconfig: "/path/to/c2"},
					C2Client1: ClusterRef{Kubeconfig: "/path/to/c2-client"},
				},
			},
			"/path/to/c2-client",
		},
		{
			"c2-client-1 empty falls back to c2",
			Config{
				Clusters: ClusterConfig{
					C2: ClusterRef{Kubeconfig: "/path/to/c2"},
				},
			},
			"/path/to/c2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.EffectiveC2Client()
			if got.Kubeconfig != tt.want {
				t.Errorf("EffectiveC2Client().Kubeconfig = %q, want %q", got.Kubeconfig, tt.want)
			}
		})
	}
}

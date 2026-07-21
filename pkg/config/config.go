package config

import (
	"fmt"

	"github.com/spf13/viper"
)

type ClusterRef struct {
	Kubeconfig string `json:"kubeconfig" mapstructure:"kubeconfig"`
}

type ClusterConfig struct {
	Hub        ClusterRef `json:"hub" mapstructure:"hub"`
	HubPassive ClusterRef `json:"hub-passive" mapstructure:"hub-passive"`
	C1         ClusterRef `json:"c1" mapstructure:"c1"`
	C2         ClusterRef `json:"c2" mapstructure:"c2"`
	C1Client1  ClusterRef `json:"c1-client-1" mapstructure:"c1-client-1"`
	C2Client1  ClusterRef `json:"c2-client-1" mapstructure:"c2-client-1"`
}

type CheckConfig struct {
	SkipHostNetworkCheck bool `json:"skip-host-network-check" mapstructure:"skip-host-network-check"`
	SkipS3Check          bool `json:"skip-s3-check" mapstructure:"skip-s3-check"`
	SkipProviderCheck    bool `json:"skip-provider-check" mapstructure:"skip-provider-check"`
}

type Config struct {
	Clusters     ClusterConfig `json:"clusters" mapstructure:"clusters"`
	Checks       CheckConfig   `json:"checks" mapstructure:"checks"`
	TestPodImage string        `json:"test-pod-image" mapstructure:"test-pod-image"`
}

func ReadConfig(filename string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(filename)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config %q: %w", filename, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Clusters.Hub.Kubeconfig == "" {
		return fmt.Errorf("clusters.hub.kubeconfig is required")
	}

	if c.Clusters.C1.Kubeconfig == "" {
		return fmt.Errorf("clusters.c1.kubeconfig is required")
	}

	if c.Clusters.C2.Kubeconfig == "" {
		return fmt.Errorf("clusters.c2.kubeconfig is required")
	}

	return nil
}

func (c *Config) EffectiveC1Client() ClusterRef {
	if c.Clusters.C1Client1.Kubeconfig != "" {
		return c.Clusters.C1Client1
	}

	return c.Clusters.C1
}

func (c *Config) EffectiveC2Client() ClusterRef {
	if c.Clusters.C2Client1.Kubeconfig != "" {
		return c.Clusters.C2Client1
	}

	return c.Clusters.C2
}

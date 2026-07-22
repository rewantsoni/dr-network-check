package cluster

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/rewantsoni/dr-network-check/pkg/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	NamespaceStorage  = "openshift-storage"
	NamespaceOperator = "openshift-operators"
)

type SubmarinerStatus struct {
	Enabled   bool
	GlobalNet bool
}

type ProxyConfig struct {
	Configured bool
	NoProxy    []string
	Env        string
	ProxyURL   string
}

type Cluster struct {
	Name       string
	Kubeconfig string
	Namespace  string
	RestConfig *rest.Config
	Clientset  kubernetes.Interface
	Client     ctrlclient.Client
	Submariner SubmarinerStatus
	Proxy      ProxyConfig
}

type Clusters struct {
	Hub        *Cluster
	HubPassive *Cluster
	C1         *Cluster
	C2         *Cluster
	C1Client1  *Cluster
	C2Client1  *Cluster
}

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = routev1.Install(s)
	_ = configv1.Install(s)

	return s
}

func NewCluster(name, kubeconfigPath string) (*Cluster, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("building rest config for %s: %w", name, err)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating clientset for %s: %w", name, err)
	}

	client, err := ctrlclient.New(restCfg, ctrlclient.Options{Scheme: newScheme()})
	if err != nil {
		return nil, fmt.Errorf("creating controller client for %s: %w", name, err)
	}

	return &Cluster{
		Name:       name,
		Kubeconfig: kubeconfigPath,
		RestConfig: restCfg,
		Clientset:  clientset,
		Client:     client,
	}, nil
}

func NewClusters(cfg *config.Config) (*Clusters, error) {
	hub, err := NewCluster("hub", cfg.Clusters.Hub.Kubeconfig)
	if err != nil {
		return nil, err
	}

	hub.Namespace = NamespaceOperator

	var hubPassive *Cluster
	if cfg.Clusters.HubPassive.Kubeconfig != "" {
		hubPassive, err = NewCluster("hub-passive", cfg.Clusters.HubPassive.Kubeconfig)
		if err != nil {
			return nil, err
		}

		hubPassive.Namespace = NamespaceOperator
	}

	c1, err := NewCluster("c1", cfg.Clusters.C1.Kubeconfig)
	if err != nil {
		return nil, err
	}

	c1.Namespace = NamespaceStorage

	c2, err := NewCluster("c2", cfg.Clusters.C2.Kubeconfig)
	if err != nil {
		return nil, err
	}

	c2.Namespace = NamespaceStorage

	var c1Client1, c2Client1 *Cluster

	effectiveC1 := cfg.EffectiveC1Client()
	if effectiveC1.Kubeconfig == cfg.Clusters.C1.Kubeconfig {
		c1Client1 = c1
	} else {
		c1Client1, err = NewCluster("c1-client-1", effectiveC1.Kubeconfig)
		if err != nil {
			return nil, err
		}

		c1Client1.Namespace = NamespaceStorage
	}

	effectiveC2 := cfg.EffectiveC2Client()
	if effectiveC2.Kubeconfig == cfg.Clusters.C2.Kubeconfig {
		c2Client1 = c2
	} else {
		c2Client1, err = NewCluster("c2-client-1", effectiveC2.Kubeconfig)
		if err != nil {
			return nil, err
		}

		c2Client1.Namespace = NamespaceStorage
	}

	return &Clusters{
		Hub:        hub,
		HubPassive: hubPassive,
		C1:         c1,
		C2:         c2,
		C1Client1:  c1Client1,
		C2Client1:  c2Client1,
	}, nil
}

func (c *Clusters) All() []*Cluster {
	seen := map[string]bool{}
	var out []*Cluster

	for _, cl := range []*Cluster{c.Hub, c.HubPassive, c.C1, c.C2, c.C1Client1, c.C2Client1} {
		if cl != nil && !seen[cl.Kubeconfig] {
			seen[cl.Kubeconfig] = true
			out = append(out, cl)
		}
	}

	return out
}

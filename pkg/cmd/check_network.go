package cmd

import (
	"context"
	"os"
	"os/signal"

	"github.com/rewantsoni/dr-network-check/pkg/check"
	"github.com/rewantsoni/dr-network-check/pkg/cluster"
	"github.com/rewantsoni/dr-network-check/pkg/config"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	"github.com/spf13/cobra"
)

var CheckNetworkCmd = &cobra.Command{
	Use:   "check-network",
	Short: "Check network connectivity between DR clusters",
	Long: `Check network connectivity required for ODF disaster recovery:
  - Ceph daemon port reachability between ODF clusters c1 and c2
  - S3 route reachability from hub and client clusters
  - OCS provider server connectivity between base clusters
  - Proxy/noProxy configuration for S3 and provider endpoints`,
	Run: runCheckNetwork,
}

func runCheckNetwork(cmd *cobra.Command, args []string) {
	cfg, err := config.ReadConfig(ConfigFile)
	if err != nil {
		console.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if cfg.TestPodImage != "" {
		check.TestPodImage = cfg.TestPodImage
	}

	console.Info("Building cluster connections")

	clusters, err := cluster.NewClusters(cfg)
	if err != nil {
		console.Fatal(err)
	}

	report := &check.CheckReport{}

	for _, cl := range clusters.All() {
		if err := check.FetchProxyConfig(ctx, cl); err != nil {
			console.Warn("Could not fetch proxy config on %s: %v", cl.Name, err)
		}
	}

	if result := check.DetectSubmariner(ctx, clusters.C1); result != nil {
		report.Add(*result)
	}

	if result := check.DetectSubmariner(ctx, clusters.C2); result != nil {
		report.Add(*result)
	}

	logSubmarinerStatus(clusters.C1)
	logSubmarinerStatus(clusters.C2)

	if cfg.Checks.SkipCephDaemonCheck {
		console.Info("Skipping ceph daemon port checks (skip-ceph-daemon-check is set)")
	} else {
		hostResults := check.CheckCephDaemonPorts(ctx, clusters.C1, clusters.C2, clusters.Hub)
		report.Add(hostResults...)
	}

	if cfg.Checks.SkipS3Check {
		console.Info("Skipping S3 route checks (skip-s3-check is set)")
	} else {
		_, s3Results := check.CheckS3Routes(ctx, clusters)
		report.Add(s3Results...)
	}

	if cfg.Checks.SkipProviderCheck {
		console.Info("Skipping OCS provider server checks (skip-provider-check is set)")
	} else {
		providerResults := check.CheckOCSProvider(ctx, clusters)
		report.Add(providerResults...)
	}

	report.Print()

	if report.HasFailures() {
		os.Exit(1)
	}
}

func logSubmarinerStatus(cl *cluster.Cluster) {
	if !cl.Submariner.Enabled {
		console.Step("%s: Submariner not detected", cl.Name)
		return
	}

	if cl.Submariner.GlobalNet {
		console.Step("%s: Submariner with GlobalNet", cl.Name)
	} else {
		console.Step("%s: Submariner without GlobalNet", cl.Name)
	}
}

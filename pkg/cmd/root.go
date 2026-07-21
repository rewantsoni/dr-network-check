package cmd

import (
	"github.com/rewantsoni/dr-network-check/pkg/build"
	"github.com/spf13/cobra"
)

var ConfigFile string

var RootCmd = &cobra.Command{
	Use:     "dr-network-check",
	Short:   "Network diagnostics for ODF disaster recovery",
	Version: build.Version,
}

func init() {
	RootCmd.SetVersionTemplate("{{.Version}}\n")
	RootCmd.PersistentFlags().StringVarP(&ConfigFile, "config", "c", "config.yaml", "configuration file")
}

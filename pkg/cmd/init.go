package cmd

import (
	"github.com/rewantsoni/dr-network-check/pkg/config"
	"github.com/rewantsoni/dr-network-check/pkg/console"
	"github.com/spf13/cobra"
)

var InitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a sample configuration file",
	Long:  "Create a sample config.yaml with placeholder kubeconfig paths for your clusters.",
	Run: func(c *cobra.Command, args []string) {
		if err := config.CreateSampleConfig(ConfigFile); err != nil {
			console.Fatal(err)
		}

		console.Completed("Created %q - edit the file with your cluster kubeconfig paths", ConfigFile)
	},
}

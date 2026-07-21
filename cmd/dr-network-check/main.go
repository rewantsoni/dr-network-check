package main

import (
	"os"

	"github.com/rewantsoni/dr-network-check/pkg/cmd"
)

func main() {
	cmd.RootCmd.AddCommand(
		cmd.InitCmd,
		cmd.CheckNetworkCmd,
	)

	if err := cmd.RootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

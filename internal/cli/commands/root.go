package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func Execute(version string) error {
	root := &cobra.Command{
		Use:   "hatchway",
		Short: "CLI-first self-hosted public tunnel system",
	}

	root.AddCommand(versionCmd(version))
	root.AddCommand(serverCmd())
	root.AddCommand(authCmd())
	root.AddCommand(httpCmd())
	root.AddCommand(listCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(tcpCmd())
	root.AddCommand(udpCmd())

	return root.Execute()
}

func versionCmd(v string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(v)
		},
	}
}

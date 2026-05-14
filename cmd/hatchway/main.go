package main

import (
	"os"

	"github.com/zydo/hatchway/internal/cli/commands"
)

var version = "dev"

func main() {
	if err := commands.Execute(version); err != nil {
		os.Exit(1)
	}
}

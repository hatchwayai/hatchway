package main

import (
	"log/slog"
	"os"

	"github.com/zydo/hatchway/internal/cli/commands"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := commands.Execute(version); err != nil {
		os.Exit(1)
	}
}

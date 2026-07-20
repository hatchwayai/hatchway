package commands

import (
	"testing"

	"github.com/spf13/cobra"
)

func findByName(cmd *cobra.Command, name string) *cobra.Command {
	for _, c := range cmd.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestServerUserCmd_FlagsAttachedToCorrectSubcommand(t *testing.T) {
	cmd := serverUserCmd()

	create := findByName(cmd, "create")
	list := findByName(cmd, "list")
	if create == nil {
		t.Fatal("missing create subcommand")
	}
	if list == nil {
		t.Fatal("missing list subcommand")
	}

	for _, name := range []string{"email", "name", "admin"} {
		if create.Flags().Lookup(name) == nil {
			t.Errorf("create subcommand missing --%s flag", name)
		}
	}
	if list.Flags().Lookup("json") == nil {
		t.Error("list subcommand missing --json flag")
	}
	// Regression: these flags were previously attached via cmd.Commands()[N],
	// which relies on cobra's alphabetical command sort and silently attaches
	// to the wrong subcommand if sort order changes.
	if list.Flags().Lookup("email") != nil {
		t.Error("list subcommand should not have --email flag")
	}
	if create.Flags().Lookup("json") != nil {
		t.Error("create subcommand should not have --json flag")
	}
}

func TestServerTokenCmd_FlagsAttachedToCorrectSubcommand(t *testing.T) {
	cmd := serverTokenCmd()

	create := findByName(cmd, "create")
	if create == nil {
		t.Fatal("missing create subcommand")
	}
	for _, name := range []string{"user", "name"} {
		if create.Flags().Lookup(name) == nil {
			t.Errorf("create subcommand missing --%s flag", name)
		}
	}
}

func TestServerTunnelsCmd_HasJSONFlag(t *testing.T) {
	// Regression: docs/cli.md documents `hatchway server tunnels --json`,
	// but the flag was never registered, so cobra rejected it at parse time.
	cmd := serverTunnelsCmd()
	if cmd.Flags().Lookup("json") == nil {
		t.Error("missing --json flag")
	}
}

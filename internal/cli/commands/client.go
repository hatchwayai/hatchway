package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	cli "github.com/zydo/hatchway/internal/cli"
)

const maxFRPCRestarts = 3

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage client authentication",
	}

	cmd.AddCommand(authSetTokenCmd())
	cmd.AddCommand(authWhoamiCmd())
	cmd.AddCommand(authLogoutCmd())

	return cmd
}

func authSetTokenCmd() *cobra.Command {
	var server string

	cmd := &cobra.Command{
		Use:   "set-token [token]",
		Short: "Save an API token for client use",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token := ""
			if len(args) > 0 {
				token = args[0]
			} else if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprint(os.Stderr, "Paste token: ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				if err != nil {
					return fmt.Errorf("read token: %w", err)
				}
				fmt.Fprintln(os.Stderr)
				token = strings.TrimSpace(string(b))
			} else {
				_, _ = fmt.Fscanln(os.Stdin, &token)
			}
			if token == "" {
				return fmt.Errorf("token is required")
			}
			if server == "" {
				server = os.Getenv("HATCHWAY_SERVER")
			}
			if server == "" {
				return fmt.Errorf("--server is required (or set HATCHWAY_SERVER)")
			}

			cred := &cli.Credentials{Server: server, Token: token}
			if err := cli.WriteCredentials(cred); err != nil {
				return err
			}

			fmt.Fprintln(os.Stderr, "Token saved.")
			return nil
		},
	}

	cmd.Flags().StringVar(&server, "server", "", "Server URL (e.g. https://api.example.com)")
	return cmd
}

func authWhoamiCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Verify the saved token",
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := cli.ResolveCredentials()
			if err != nil {
				return err
			}

			client := cli.NewClient(cred)
			userID, err := client.Whoami()
			if err != nil {
				return err
			}

			if asJSON {
				out, _ := json.Marshal(map[string]string{
					"user_id": userID,
					"server":  cred.Server,
				})
				fmt.Println(string(out))
			} else {
				fmt.Fprintf(os.Stderr, "Authenticated as %s on %s\n", userID, cred.Server)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cli.DeleteCredentials(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Token removed.")
			return nil
		},
	}
}

func httpCmd() *cobra.Command {
	var ttl string
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "http <port>",
		Short: "Create an HTTP tunnel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[0])
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("invalid port: %s", args[0])
			}

			cred, err := cli.ResolveCredentials()
			if err != nil {
				return err
			}

			// Pre-flight: check local port is reachable
			if err := cli.CheckLocalPort(port); err != nil {
				return err
			}

			ttlDuration := 3600 * time.Second
			if ttl != "" {
				ttlDuration, err = cli.ParseTTL(ttl)
				if err != nil {
					return err
				}
			}

			const localHost = "127.0.0.1"

			client := cli.NewClient(cred)
			idempotencyKey := uuid.New().String()
			tunnel, err := client.CreateTunnel(&cli.CreateTunnelRequest{
				Type:       "http",
				LocalHost:  localHost,
				LocalPort:  port,
				TTLSeconds: int(ttlDuration.Seconds()),
			}, idempotencyKey)
			if err != nil {
				return fmt.Errorf("create tunnel: %w", err)
			}

			if asJSON {
				out, _ := json.Marshal(map[string]string{
					"tunnel_id":  tunnel.TunnelID,
					"public_url": tunnel.PublicURL,
					"status":     tunnel.Status,
				})
				fmt.Println(string(out))
			} else {
				fmt.Fprintf(os.Stderr, "Tunnel created: %s\n", tunnel.PublicURL)
			}

			// Generate frpc.toml in temp dir
			frpcConfig := generateFRPCConfig(tunnel, localHost, port)
			tmpDir, err := os.MkdirTemp("", "hatchway-frpc-*")
			if err != nil {
				return fmt.Errorf("create temp dir: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			configPath := filepath.Join(tmpDir, "frpc.toml")
			if err := os.WriteFile(configPath, []byte(frpcConfig), 0600); err != nil {
				return fmt.Errorf("write frpc config: %w", err)
			}

			// Spawn frpc subprocess
			frpcPath, err := exec.LookPath("frpc")
			if err != nil {
				fmt.Fprintln(os.Stderr, "Warning: frpc not found in PATH. Tunnel is reserved but not connected.")
				fmt.Fprintln(os.Stderr, "Install frp or use the tunnel manually with the config below:")
				fmt.Fprintln(os.Stderr, frpcConfig)
				return nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			runFRPC := func() *exec.Cmd {
				c := exec.CommandContext(ctx, frpcPath, "-c", configPath)
				c.Stderr = os.Stderr
				return c
			}

			frpcCmd := runFRPC()
			if err := frpcCmd.Start(); err != nil {
				return fmt.Errorf("start frpc: %w", err)
			}

			cleanup := func() {
				cancel()
				// Best-effort revoke with a short deadline so Ctrl-C never
				// hangs. Use a context detached from the cancelled `ctx`
				// above so the request can actually go out.
				deleteCtx, deleteCancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer deleteCancel()
				_ = client.DeleteTunnelCtx(deleteCtx, tunnel.TunnelID)
			}

			restarts := 0
			for {
				doneCh := make(chan error, 1)
				go func() { doneCh <- frpcCmd.Wait() }()

				select {
				case sig := <-sigCh:
					fmt.Fprintf(os.Stderr, "\nReceived %s, cleaning up...\n", sig)
					cleanup()
					return nil
				case err := <-doneCh:
					if err == nil {
						return nil
					}
					slog.Warn("frpc exited unexpectedly", "error", err, "restart", restarts+1, "max", maxFRPCRestarts)
					if restarts >= maxFRPCRestarts {
						cleanup()
						return fmt.Errorf("frpc failed after %d restarts: %w", maxFRPCRestarts, err)
					}
					restarts++
					backoff := time.Duration(restarts) * 2 * time.Second
					fmt.Fprintf(os.Stderr, "frpc exited unexpectedly (restart %d/%d after %s)...\n", restarts, maxFRPCRestarts, backoff)
					select {
					case <-time.After(backoff):
					case <-sigCh:
						cleanup()
						return nil
					}
					frpcCmd = runFRPC()
					if err := frpcCmd.Start(); err != nil {
						cleanup()
						return fmt.Errorf("restart frpc: %w", err)
					}
				}
			}
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Tunnel TTL (e.g. 15m, 1h, 24h)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func listCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active tunnels",
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := cli.ResolveCredentials()
			if err != nil {
				return err
			}

			client := cli.NewClient(cred)
			list, err := client.ListTunnels(0)
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(list.Tunnels)
			}

			if len(list.Tunnels) == 0 {
				fmt.Fprintln(os.Stderr, "No tunnels found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tSTATUS\tURL")
			for _, t := range list.Tunnels {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.TunnelID, t.Type, t.Status, t.PublicURL)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func deleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <tunnel_id>",
		Short: "Delete a tunnel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := cli.ResolveCredentials()
			if err != nil {
				return err
			}

			client := cli.NewClient(cred)
			if err := client.DeleteTunnel(args[0]); err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "Tunnel %s deleted.\n", args[0])
			return nil
		},
	}
}

func tcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tcp <port>",
		Short: "Create a TCP tunnel (not yet supported)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("TCP tunnels are not yet supported in this version")
		},
	}
}

func udpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "udp <port>",
		Short: "Create a UDP tunnel (not yet supported)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("UDP tunnels are not yet supported in this version")
		},
	}
}

func generateFRPCConfig(tunnel *cli.TunnelResponse, localHost string, localPort int) string {
	if localHost == "" {
		localHost = "127.0.0.1"
	}
	return fmt.Sprintf(`serverAddr = "%s"
serverPort = %d

auth.method = "token"
auth.token = "%s"

metadatas.runtime_token = "%s"

[[proxies]]
name = "%s"
type = "%s"
localIP = "%s"
localPort = %d
subdomain = "%s"
`, tunnel.FRP.ServerAddr, tunnel.FRP.ServerPort,
		tunnel.FRP.ServerToken,
		tunnel.RuntimeToken,
		tunnel.TunnelID, tunnel.FRP.ProxyType,
		localHost, localPort,
		tunnel.TunnelID,
	)
}

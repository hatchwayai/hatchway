package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/zydo/hatchway/internal/config"
	"github.com/zydo/hatchway/internal/db"
	frp "github.com/zydo/hatchway/internal/frp"
	"github.com/zydo/hatchway/internal/models"
	"github.com/zydo/hatchway/internal/server/api"
	"github.com/zydo/hatchway/internal/server/plugin"
	"github.com/zydo/hatchway/internal/server/tunnels"
	"github.com/zydo/hatchway/internal/tokens"
)

func serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Server-side operations",
	}

	cmd.AddCommand(serverInitCmd())
	cmd.AddCommand(serverRunCmd())
	cmd.AddCommand(serverUserCmd())
	cmd.AddCommand(serverTokenCmd())
	cmd.AddCommand(serverTunnelsCmd())

	return cmd
}

func serverInitCmd() *cobra.Command {
	var force bool
	var assumeYes bool
	var adminEmail string
	var adminName string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the database and create an admin user",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}

			if err := db.RunMigrations(cfg.DatabaseURL); err != nil {
				return fmt.Errorf("run migrations: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Migrations applied.")

			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			// Check if any user exists
			var userCount int
			if err := database.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
				return fmt.Errorf("check existing users: %w", err)
			}
			if userCount > 0 && !force {
				return fmt.Errorf("database already has users. Use --force to reinitialize")
			}
			if userCount > 0 && force && !assumeYes {
				fmt.Fprintf(os.Stderr,
					"--force will create a new admin user alongside the existing %d user(s); existing API tokens remain valid.\n",
					userCount,
				)
				fmt.Fprint(os.Stderr, "Type 'yes' to continue: ")
				reader := bufio.NewReader(os.Stdin)
				answer, _ := reader.ReadString('\n')
				if strings.TrimSpace(strings.ToLower(answer)) != "yes" {
					return fmt.Errorf("aborted")
				}
			}

			// Mint first API token
			tok, err := tokens.MintAPIToken()
			if err != nil {
				return fmt.Errorf("mint token: %w", err)
			}

			// Create the admin user and its token together — a failure
			// between the two would otherwise leave an admin user with no
			// usable token.
			userID := uuid.New().String()
			tx, err := database.Pool.Begin(ctx)
			if err != nil {
				return fmt.Errorf("begin transaction: %w", err)
			}
			defer func() { _ = tx.Rollback(ctx) }()

			if _, err := tx.Exec(ctx,
				"INSERT INTO users (id, email, name, is_admin) VALUES ($1, $2, $3, true)",
				userID, adminEmail, adminName,
			); err != nil {
				return fmt.Errorf("create admin user: %w", err)
			}

			if _, err := tx.Exec(ctx,
				"INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, $3, $4, $5)",
				uuid.New().String(), userID, "init-token", tok.Prefix, tok.Hash,
			); err != nil {
				return fmt.Errorf("store token: %w", err)
			}

			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit transaction: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Admin user created: %s\n", adminEmail)
			fmt.Fprintln(os.Stderr, "API token (save this — it won't be shown again):")
			fmt.Println(tok.Raw)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Allow init when users already exist")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip the --force confirmation prompt")
	cmd.Flags().StringVar(&adminEmail, "admin-email", "admin@hatchway.local", "Email for the bootstrap admin user")
	cmd.Flags().StringVar(&adminName, "admin-name", "admin", "Display name for the bootstrap admin user")
	return cmd
}

func serverRunCmd() *cobra.Command {
	var dev bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the Hatchway server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}

			if dev {
				cfg.FRPSMode = "subprocess"
			}

			if err := cfg.Validate(); err != nil {
				return err
			}

			// Single cancellable ctx for the whole server lifetime so SIGINT/
			// SIGTERM also stops the reaper, sweepers, and frps subprocess —
			// not just the HTTP listeners.
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			tunnelRoutes := func(r chi.Router) {
				tunnels.RegisterRoutes(r, database.Pool, cfg)
			}

			transitionFn := func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
				return tunnels.Transition(ctx, pool, tunnelID, tunnels.Event(event))
			}
			pluginHandler := func() http.HandlerFunc {
				return plugin.Handler(database.Pool, cfg, transitionFn, api.GlobalMetrics.IncrPluginOp, api.GlobalMetrics.IncrPluginDeadlineExceeded)
			}

			go tunnels.StartReaper(ctx, database.Pool, 30*time.Second)
			go tunnels.StartSweepers(ctx, database.Pool, cfg.EventsRetentionDays, cfg.IdempotencyRetentionHours)
			var frpsProc *frp.Process
			if cfg.FRPSMode == "subprocess" {
				frpsProc = frp.NewProcess("frps", cfg.FRPSBinPath, []string{"-c", cfg.FRPSConfigPath})
				if err := frpsProc.Start(ctx); err != nil {
					slog.Warn("frps subprocess failed to start (non-fatal)", "error", err)
				}
			}

			err = api.StartServer(ctx, cfg, database, pluginHandler, tunnelRoutes)
			cancel() // ensure reaper/sweepers exit even if StartServer returned via errCh
			if frpsProc != nil {
				_ = frpsProc.Stop(5 * time.Second)
			}
			return err
		},
	}

	cmd.Flags().BoolVar(&dev, "dev", false, "Run frps as a subprocess (local development)")
	return cmd
}

func serverUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new user",
		RunE: func(cmd *cobra.Command, args []string) error {
			email, _ := cmd.Flags().GetString("email")
			name, _ := cmd.Flags().GetString("name")
			isAdmin, _ := cmd.Flags().GetBool("admin")
			if email == "" {
				return fmt.Errorf("--email is required")
			}

			cfg := config.Load()
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			id := uuid.New().String()
			_, err = database.Pool.Exec(ctx,
				"INSERT INTO users (id, email, name, is_admin) VALUES ($1, $2, $3, $4)",
				id, email, name, isAdmin,
			)
			if err != nil {
				return fmt.Errorf("create user: %w", err)
			}

			role := "user"
			if isAdmin {
				role = "admin"
			}
			fmt.Fprintf(os.Stderr, "User created (%s): %s (%s)\n", role, email, id)
			return nil
		},
	}
	createCmd.Flags().String("email", "", "User email")
	createCmd.Flags().String("name", "", "User display name")
	createCmd.Flags().Bool("admin", false, "Grant admin privileges")
	cmd.AddCommand(createCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all users",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			rows, err := database.Pool.Query(ctx, "SELECT id, email, name, is_admin, created_at FROM users ORDER BY created_at")
			if err != nil {
				return err
			}
			defer rows.Close()

			users := []models.User{}
			for rows.Next() {
				var u models.User
				if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.IsAdmin, &u.CreatedAt); err != nil {
					return err
				}
				users = append(users, u)
			}

			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(users)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tEMAIL\tNAME\tADMIN\tCREATED")
			for _, u := range users {
				fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", u.ID[:8], ptrStr(u.Email), ptrStr(u.Name), u.IsAdmin, u.CreatedAt.Format("2006-01-02"))
			}
			w.Flush()
			return nil
		},
	}
	listCmd.Flags().Bool("json", false, "Output as JSON")
	cmd.AddCommand(listCmd)

	return cmd
}

func serverTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			user, _ := cmd.Flags().GetString("user")
			name, _ := cmd.Flags().GetString("name")
			if user == "" || name == "" {
				return fmt.Errorf("--user and --name are required")
			}

			cfg := config.Load()
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			var userID string
			err = database.Pool.QueryRow(ctx,
				"SELECT id FROM users WHERE email = $1 OR name = $1", user,
			).Scan(&userID)
			if err != nil {
				return fmt.Errorf("user not found: %s", user)
			}

			tok, err := tokens.MintAPIToken()
			if err != nil {
				return err
			}

			_, err = database.Pool.Exec(ctx,
				"INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, $3, $4, $5)",
				uuid.New().String(), userID, name, tok.Prefix, tok.Hash,
			)
			if err != nil {
				return fmt.Errorf("store token: %w", err)
			}

			fmt.Fprintln(os.Stderr, "Token created. Save this — it won't be shown again:")
			fmt.Println(tok.Raw)
			return nil
		},
	}
	createCmd.Flags().String("user", "", "User email or name")
	createCmd.Flags().String("name", "", "Token label")
	cmd.AddCommand(createCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <token_id>",
		Short: "Revoke an API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("token ID is required")
			}

			cfg := config.Load()
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			tag, err := database.Pool.Exec(ctx,
				"UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL", args[0],
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("token not found or already revoked")
			}

			fmt.Fprintf(os.Stderr, "Token %s revoked.\n", args[0])
			return nil
		},
	})

	return cmd
}

func serverTunnelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnels",
		Short: "List tunnels (admin view across all users)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Load()
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			rows, err := database.Pool.Query(ctx,
				"SELECT id, user_id, type, status, local_host, local_port, expires_at, created_at FROM tunnels ORDER BY created_at DESC",
			)
			if err != nil {
				return err
			}
			defer rows.Close()

			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				var tunnels []models.Tunnel
				for rows.Next() {
					var t models.Tunnel
					if err := rows.Scan(&t.ID, &t.UserID, &t.Type, &t.Status, &t.LocalHost, &t.LocalPort, &t.ExpiresAt, &t.CreatedAt); err != nil {
						return err
					}
					tunnels = append(tunnels, t)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(tunnels)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tUSER\tTYPE\tSTATUS\tLOCAL\tEXPIRES")
			for rows.Next() {
				var id, userID, typ, status, localHost string
				var localPort int
				var expiresAt *time.Time
				var createdAt time.Time
				if err := rows.Scan(&id, &userID, &typ, &status, &localHost, &localPort, &expiresAt, &createdAt); err != nil {
					return err
				}
				expires := "-"
				if expiresAt != nil {
					expires = expiresAt.Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s:%d\t%s\n", id, userID[:8], typ, status, localHost, localPort, expires)
			}
			w.Flush()
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "Output as JSON")
	return cmd
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

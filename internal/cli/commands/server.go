package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/hatchwayai/hatchway/internal/config"
	"github.com/hatchwayai/hatchway/internal/db"
	frp "github.com/hatchwayai/hatchway/internal/frp"
	"github.com/hatchwayai/hatchway/internal/models"
	"github.com/hatchwayai/hatchway/internal/server/api"
	"github.com/hatchwayai/hatchway/internal/server/plugin"
	"github.com/hatchwayai/hatchway/internal/server/tunnels"
	"github.com/hatchwayai/hatchway/internal/tokens"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

const (
	defaultHealthcheckURL     = "http://127.0.0.1:9000/readyz"
	defaultHealthcheckTimeout = 5 * time.Second
	healthcheckDrainLimit     = 32 << 10
)

func serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Server-side operations",
	}

	cmd.AddCommand(serverInitCmd())
	cmd.AddCommand(serverRunCmd())
	cmd.AddCommand(serverHealthcheckCmd())
	cmd.AddCommand(serverUserCmd())
	cmd.AddCommand(serverTokenCmd())
	cmd.AddCommand(serverTunnelsCmd())

	return cmd
}

func serverHealthcheckCmd() *cobra.Command {
	var targetURL string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check whether the Hatchway server is ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return errors.New("healthcheck timeout must be positive")
			}
			client := &http.Client{
				Timeout: timeout,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			return runServerHealthcheck(cmd.Context(), client, targetURL)
		},
	}
	cmd.Flags().StringVar(&targetURL, "url", defaultHealthcheckURL, "Readiness URL to check")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultHealthcheckTimeout, "Maximum time to wait")
	return cmd
}

func runServerHealthcheck(ctx context.Context, client *http.Client, targetURL string) (returnErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil || req.URL.Host == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return errors.New("invalid healthcheck URL")
	}

	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return errors.New("healthcheck timed out")
		case errors.Is(err, context.Canceled):
			return errors.New("healthcheck canceled")
		default:
			// Deliberately omit the target and transport error: either can
			// contain URL credentials, query parameters, or proxy details.
			return errors.New("healthcheck request failed")
		}
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			returnErr = errors.Join(returnErr, errors.New("healthcheck response close failed"))
		}
	}()

	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, healthcheckDrainLimit))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned HTTP %d", resp.StatusCode)
	}
	if readErr != nil {
		return errors.New("healthcheck response read failed")
	}
	return nil
}

func serverInitCmd() *cobra.Command {
	var force bool
	var assumeYes bool
	var adminEmail string
	var adminName string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the database and create an admin user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
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

			// Bootstrap is normally one-time. --force is additive: it creates
			// another administrator and leaves existing users and tokens intact.
			var userCount int
			if err := database.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
				return fmt.Errorf("check existing users: %w", err)
			}
			if userCount > 0 && !force {
				return fmt.Errorf("database already has users; use --force to add another administrator")
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}

			if dev {
				cfg.FRPSMode = "subprocess"
			}

			if err := cfg.Validate(); err != nil {
				return err
			}

			// Apply embedded migrations before accepting traffic. This gives
			// upgrades a safe migration-only path without abusing `server init`,
			// which is specifically for creating the first admin.
			if err := db.RunMigrations(cfg.DatabaseURL); err != nil {
				return fmt.Errorf("run migrations: %w", err)
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
			if err := db.CheckSchema(ctx, database.Pool); err != nil {
				return fmt.Errorf("verify database schema: %w", err)
			}

			tunnelRoutes := func(r chi.Router) {
				tunnels.RegisterRoutes(r, database.Pool, cfg)
			}

			transitionFn := func(ctx context.Context, pool *pgxpool.Pool, tunnelID string, event string) error {
				return tunnels.Transition(ctx, pool, tunnelID, tunnels.Event(event))
			}
			pluginHandler := plugin.Handler(database.Pool, cfg, transitionFn, api.GlobalMetrics.IncrPluginOp, api.GlobalMetrics.IncrPluginDeadlineExceeded)
			tunnelAuthorizationHandler := plugin.TunnelAuthorizationHandler(database.Pool, cfg)

			tunnels.StartReaper(ctx, database.Pool, 30*time.Second)
			tunnels.StartSweepers(ctx, database.Pool, cfg.EventsRetentionDays, cfg.IdempotencyRetentionHours, cfg.RuntimeTokenRetentionDays)
			if cfg.FRPSMode != "subprocess" {
				return api.StartServer(ctx, cfg, database, pluginHandler, tunnelAuthorizationHandler, tunnelRoutes)
			}

			frpsProc := frp.NewProcess("frps", cfg.FRPSBinPath, []string{"-c", cfg.FRPSConfigPath})
			if err := frpsProc.Start(ctx); err != nil {
				return fmt.Errorf("start frps subprocess: %w", err)
			}
			defer func() { _ = frpsProc.Stop(5 * time.Second) }()

			serverErrCh := make(chan error, 1)
			go func() {
				serverErrCh <- api.StartServer(ctx, cfg, database, pluginHandler, tunnelAuthorizationHandler, tunnelRoutes)
			}()
			frpsErrCh := make(chan error, 1)
			go func() {
				frpsErrCh <- frpsProc.Wait()
			}()

			select {
			case serverErr := <-serverErrCh:
				cancel()
				return serverErr
			case frpsErr := <-frpsErrCh:
				if ctx.Err() != nil {
					return <-serverErrCh
				}
				cancel()
				<-serverErrCh
				if frpsErr == nil {
					return fmt.Errorf("frps subprocess exited unexpectedly")
				}
				return fmt.Errorf("frps subprocess exited unexpectedly: %w", frpsErr)
			case <-ctx.Done():
				return <-serverErrCh
			}
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			email, _ := cmd.Flags().GetString("email")
			name, _ := cmd.Flags().GetString("name")
			isAdmin, _ := cmd.Flags().GetBool("admin")
			if email == "" {
				return fmt.Errorf("--email is required")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
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
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate users: %w", err)
			}

			if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(users)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tEMAIL\tNAME\tADMIN\tCREATED"); err != nil {
				return fmt.Errorf("write user table header: %w", err)
			}
			for _, u := range users {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\n", u.ID[:8], ptrStr(u.Email), ptrStr(u.Name), u.IsAdmin, u.CreatedAt.Format("2006-01-02")); err != nil {
					return fmt.Errorf("write user table row: %w", err)
				}
			}
			return w.Flush()
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			user, _ := cmd.Flags().GetString("user")
			name, _ := cmd.Flags().GetString("name")
			if user == "" || name == "" {
				return fmt.Errorf("--user and --name are required")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			rows, err := database.Pool.Query(ctx,
				"SELECT id FROM users WHERE id::text = $1 OR email = $1 OR name = $1 ORDER BY id LIMIT 2",
				user,
			)
			if err != nil {
				return fmt.Errorf("look up user %q: %w", user, err)
			}
			defer rows.Close()
			var userIDs []string
			for rows.Next() {
				var userID string
				if err := rows.Scan(&userID); err != nil {
					return fmt.Errorf("scan user: %w", err)
				}
				userIDs = append(userIDs, userID)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate users: %w", err)
			}
			if len(userIDs) == 0 {
				return fmt.Errorf("user not found: %s", user)
			}
			if len(userIDs) > 1 {
				return fmt.Errorf("user selector %q is ambiguous; use the user UUID or unique email", user)
			}
			userID := userIDs[0]

			tok, err := tokens.MintAPIToken()
			if err != nil {
				return err
			}

			tokenID := uuid.New().String()
			_, err = database.Pool.Exec(ctx,
				"INSERT INTO api_tokens (id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, $3, $4, $5)",
				tokenID, userID, name, tok.Prefix, tok.Hash,
			)
			if err != nil {
				return fmt.Errorf("store token: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Token created (%s). Save the value below — it won't be shown again:\n", tokenID)
			fmt.Println(tok.Raw)
			return nil
		},
	}
	createCmd.Flags().String("user", "", "User UUID, email, or unique name")
	createCmd.Flags().String("name", "", "Token label")
	cmd.AddCommand(createCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List API token IDs and metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
			ctx := context.Background()
			database, err := db.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer database.Close()

			user, _ := cmd.Flags().GetString("user")
			rows, err := database.Pool.Query(ctx,
				`SELECT t.id, u.id, u.email, t.name, t.token_prefix, t.revoked_at, t.created_at
				   FROM api_tokens t
				   JOIN users u ON u.id = t.user_id
				  WHERE $1 = '' OR u.id::text = $1 OR u.email = $1
				  ORDER BY t.created_at DESC`,
				user,
			)
			if err != nil {
				return fmt.Errorf("list API tokens: %w", err)
			}
			defer rows.Close()

			type tokenMetadata struct {
				ID        string     `json:"id"`
				UserID    string     `json:"user_id"`
				UserEmail *string    `json:"user_email"`
				Name      string     `json:"name"`
				Prefix    string     `json:"prefix"`
				RevokedAt *time.Time `json:"revoked_at"`
				CreatedAt time.Time  `json:"created_at"`
			}
			tokens := make([]tokenMetadata, 0)
			for rows.Next() {
				var token tokenMetadata
				if err := rows.Scan(&token.ID, &token.UserID, &token.UserEmail, &token.Name, &token.Prefix, &token.RevokedAt, &token.CreatedAt); err != nil {
					return fmt.Errorf("scan API token: %w", err)
				}
				tokens = append(tokens, token)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate API tokens: %w", err)
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(tokens)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tUSER\tNAME\tPREFIX\tREVOKED\tCREATED"); err != nil {
				return fmt.Errorf("write API token table header: %w", err)
			}
			for _, token := range tokens {
				userLabel := token.UserID
				if token.UserEmail != nil {
					userLabel = *token.UserEmail
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%t\t%s\n",
					token.ID, userLabel, token.Name, token.Prefix, token.RevokedAt != nil, token.CreatedAt.Format(time.RFC3339)); err != nil {
					return fmt.Errorf("write API token table row: %w", err)
				}
			}
			return w.Flush()
		},
	}
	listCmd.Flags().String("user", "", "Filter by user UUID or email")
	listCmd.Flags().Bool("json", false, "Output as JSON")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <token_id>",
		Short: "Revoke an API token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.DatabaseURL == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
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
				tunnels := make([]models.Tunnel, 0)
				for rows.Next() {
					var t models.Tunnel
					if err := rows.Scan(&t.ID, &t.UserID, &t.Type, &t.Status, &t.LocalHost, &t.LocalPort, &t.ExpiresAt, &t.CreatedAt); err != nil {
						return err
					}
					tunnels = append(tunnels, t)
				}
				if err := rows.Err(); err != nil {
					return fmt.Errorf("iterate tunnels: %w", err)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(tunnels)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tUSER\tTYPE\tSTATUS\tLOCAL\tEXPIRES"); err != nil {
				return fmt.Errorf("write tunnel table header: %w", err)
			}
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
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s:%d\t%s\n", id, userID[:8], typ, status, localHost, localPort, expires); err != nil {
					return fmt.Errorf("write tunnel table row: %w", err)
				}
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate tunnels: %w", err)
			}
			return w.Flush()
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

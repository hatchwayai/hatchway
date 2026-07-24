package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	credFileName = "credentials.json"
	credDirPerm  = 0700
	credFilePerm = 0600
)

// Credentials stores the client authentication state.
type Credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// CredPath returns the path to the credentials file. Callers performing I/O
// use credentialPath so home-directory lookup failures are not ignored.
func CredPath() string {
	path, err := credentialPath()
	if err != nil {
		// Preserve the historical, side-effect-free helper behavior when the
		// process has no discoverable home directory. I/O paths use
		// credentialPath directly and surface the lookup error.
		return filepath.Join(".config", "hatchway", credFileName)
	}
	return path
}

func credentialPath() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "hatchway", credFileName), nil
}

// ReadCredentials loads credentials from file.
// Returns nil, nil if the file does not exist.
func ReadCredentials() (*Credentials, error) {
	path, err := credentialPath()
	if err != nil {
		return nil, err
	}
	if err := checkCredentialDir(filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := checkFilePerms(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// #nosec G304 -- the path is the fixed credentials filename beneath the
	// validated, private configuration directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	var cred Credentials
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &cred, nil
}

// WriteCredentials atomically saves credentials to a regular file with mode
// 0600, replacing insecure modes from older versions.
func WriteCredentials(cred *Credentials) error {
	if cred == nil {
		return fmt.Errorf("credentials are required")
	}
	normalizedServer, err := normalizeServerURL(cred.Server)
	if err != nil {
		return err
	}
	stored := *cred
	stored.Server = normalizedServer

	path, err := credentialPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, credDirPerm); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	if err := checkCredentialDir(dir); err != nil {
		return err
	}
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("credentials path %s must not be a symbolic link", path)
		}
		if !existing.Mode().IsRegular() {
			return fmt.Errorf("credentials path %s is not a regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect credentials path: %w", err)
	}

	data, err := json.MarshalIndent(&stored, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credentials file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(credFilePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary credentials file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close credentials: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}

	return nil
}

// DeleteCredentials removes the credentials file.
func DeleteCredentials() error {
	path, pathErr := credentialPath()
	if pathErr != nil {
		return pathErr
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete credentials: %w", err)
	}
	return nil
}

// ResolveCredentials returns credentials with env var overrides applied.
// HATCHWAY_TOKEN and HATCHWAY_SERVER take precedence over the file.
func ResolveCredentials() (*Credentials, error) {
	server := os.Getenv("HATCHWAY_SERVER")
	token := os.Getenv("HATCHWAY_TOKEN")

	var cred *Credentials
	if server != "" && token != "" {
		// Complete environment overrides do not depend on an optional file.
		cred = &Credentials{}
	} else {
		var err error
		cred, err = ReadCredentials()
		if err != nil {
			return nil, err
		}
	}

	if cred == nil && server == "" && token == "" {
		return nil, fmt.Errorf("not authenticated: run 'hatchway auth set-token' or set HATCHWAY_TOKEN and HATCHWAY_SERVER")
	}

	if cred == nil {
		cred = &Credentials{}
	}

	if server != "" {
		cred.Server = server
	}
	if token != "" {
		cred.Token = token
	}

	if cred.Server == "" {
		return nil, fmt.Errorf("no server configured: set HATCHWAY_SERVER or run 'hatchway auth set-token --server <url>'")
	}
	if cred.Token == "" {
		return nil, fmt.Errorf("no token configured: set HATCHWAY_TOKEN or run 'hatchway auth set-token'")
	}
	normalizedServer, err := normalizeServerURL(cred.Server)
	if err != nil {
		return nil, err
	}
	cred.Server = normalizedServer

	return cred, nil
}

func checkFilePerms(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("credentials path %s must be a regular file, not a symbolic link", path)
	}
	if info.Mode().Perm()&0066 != 0 {
		return fmt.Errorf("credentials file %s has overly permissive permissions (%o), expected %o", path, info.Mode().Perm(), credFilePerm)
	}
	return nil
}

func checkCredentialDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("config path %s must be a directory, not a symbolic link", dir)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config dir %s has overly permissive permissions (%o), expected %o", dir, info.Mode().Perm(), credDirPerm)
	}
	return nil
}

func normalizeServerURL(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid Hatchway server URL %q: %w", raw, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid Hatchway server URL %q: use http:// or https:// with a host", raw)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("invalid Hatchway server URL %q: user info, paths, queries, and fragments are not supported", raw)
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

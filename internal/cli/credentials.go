package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// CredPath returns the path to the credentials file.
func CredPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "hatchway", credFileName)
}

// ReadCredentials loads credentials from file.
// Returns nil, nil if the file does not exist.
func ReadCredentials() (*Credentials, error) {
	path := CredPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	if err := checkFilePerms(path); err != nil {
		return nil, err
	}

	var cred Credentials
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &cred, nil
}

// WriteCredentials saves credentials to disk with correct permissions.
func WriteCredentials(cred *Credentials) error {
	path := CredPath()
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, credDirPerm); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Check if directory has overly permissive permissions
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat config dir: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config dir %s has overly permissive permissions (%o), expected %o", dir, info.Mode().Perm(), credDirPerm)
	}

	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	if err := os.WriteFile(path, data, credFilePerm); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	return nil
}

// DeleteCredentials removes the credentials file.
func DeleteCredentials() error {
	path := CredPath()
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete credentials: %w", err)
	}
	return nil
}

// ResolveCredentials returns credentials with env var overrides applied.
// HATCHWAY_TOKEN and HATCHWAY_SERVER take precedence over the file.
func ResolveCredentials() (*Credentials, error) {
	cred, err := ReadCredentials()
	if err != nil {
		return nil, err
	}

	server := os.Getenv("HATCHWAY_SERVER")
	token := os.Getenv("HATCHWAY_TOKEN")

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

	return cred, nil
}

func checkFilePerms(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0066 != 0 {
		return fmt.Errorf("credentials file %s has overly permissive permissions (%o), expected %o", path, info.Mode().Perm(), credFilePerm)
	}
	return nil
}

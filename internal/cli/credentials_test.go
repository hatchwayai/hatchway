package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCredPath(t *testing.T) {
	os.Clearenv()
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "hatchway", "credentials.json")
	if got := CredPath(); got != expected {
		t.Errorf("CredPath() = %q, want %q", got, expected)
	}
}

func TestCredPathXDG(t *testing.T) {
	os.Clearenv()
	os.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	expected := filepath.Join("/tmp/xdg-test", "hatchway", "credentials.json")
	if got := CredPath(); got != expected {
		t.Errorf("CredPath() = %q, want %q", got, expected)
	}
}

func TestWriteAndReadCredentials(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	cred := &Credentials{Server: "https://api.example.com", Token: "sk_live_test123"}
	if err := WriteCredentials(cred); err != nil {
		t.Fatalf("WriteCredentials: %v", err)
	}

	// Verify file exists and has correct permissions
	path := CredPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Verify directory permissions
	dir := filepath.Dir(path)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Errorf("dir permissions = %o, want 0700", dirInfo.Mode().Perm())
	}

	// Read back
	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("ReadCredentials: %v", err)
	}
	if got.Server != cred.Server {
		t.Errorf("server = %q, want %q", got.Server, cred.Server)
	}
	if got.Token != cred.Token {
		t.Errorf("token = %q, want %q", got.Token, cred.Token)
	}
}

func TestReadCredentialsNoFile(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("ReadCredentials: %v", err)
	}
	if got != nil {
		t.Error("expected nil for missing file")
	}
}

func TestDeleteCredentials(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	cred := &Credentials{Server: "https://example.com", Token: "test"}
	_ = WriteCredentials(cred)

	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}

	got, _ := ReadCredentials()
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteCredentialsIdempotent(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Delete on non-existent file should not error
	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials on missing file: %v", err)
	}
}

func TestResolveCredentialsFromEnv(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.Setenv("HATCHWAY_SERVER", "https://from-env.example.com")
	os.Setenv("HATCHWAY_TOKEN", "sk_live_envtoken")

	cred, err := ResolveCredentials()
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if cred.Server != "https://from-env.example.com" {
		t.Errorf("server = %q", cred.Server)
	}
	if cred.Token != "sk_live_envtoken" {
		t.Errorf("token = %q", cred.Token)
	}
}

func TestResolveCredentialsEnvOverridesFile(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.Setenv("HATCHWAY_SERVER", "https://override.example.com")
	os.Setenv("HATCHWAY_TOKEN", "sk_live_override")

	// Write file credentials
	_ = WriteCredentials(&Credentials{Server: "https://file.example.com", Token: "sk_live_file"})

	cred, err := ResolveCredentials()
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if cred.Server != "https://override.example.com" {
		t.Errorf("server = %q, want override", cred.Server)
	}
	if cred.Token != "sk_live_override" {
		t.Errorf("token = %q, want override", cred.Token)
	}
}

func TestResolveCredentialsNoAuth(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	_, err := ResolveCredentials()
	if err == nil {
		t.Error("expected error when not authenticated")
	}
}

func TestResolveCredentialsMissingServer(t *testing.T) {
	os.Clearenv()
	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	os.Setenv("HATCHWAY_TOKEN", "sk_live_test")

	_, err := ResolveCredentials()
	if err == nil {
		t.Error("expected error when server is missing")
	}
}

func TestCredentialsJSON(t *testing.T) {
	cred := &Credentials{Server: "https://example.com", Token: "sk_live_abc"}
	data, err := json.Marshal(cred)
	if err != nil {
		t.Fatal(err)
	}

	var got Credentials
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != *cred {
		t.Errorf("round-trip failed: %+v", got)
	}
}

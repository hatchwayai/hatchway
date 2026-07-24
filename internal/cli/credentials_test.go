package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func clearCredentialEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HATCHWAY_SERVER", "")
	t.Setenv("HATCHWAY_TOKEN", "")
}

func TestCredPath(t *testing.T) {
	clearCredentialEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("find user home directory: %v", err)
	}
	expected := filepath.Join(home, ".config", "hatchway", "credentials.json")
	if got := CredPath(); got != expected {
		t.Errorf("CredPath() = %q, want %q", got, expected)
	}
}

func TestCredPathXDG(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	expected := filepath.Join("/tmp/xdg-test", "hatchway", "credentials.json")
	if got := CredPath(); got != expected {
		t.Errorf("CredPath() = %q, want %q", got, expected)
	}
}

func TestWriteAndReadCredentials(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

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

func TestWriteCredentialsReplacesInsecureFileAtomically(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	path := filepath.Join(tmpDir, "hatchway", credFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"server":"https://old.example","token":"old"}`), 0644); err != nil {
		t.Fatal(err)
	}

	want := &Credentials{Server: "https://new.example/", Token: "new"}
	if err := WriteCredentials(want); err != nil {
		t.Fatalf("WriteCredentials() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != credFilePerm {
		t.Fatalf("credentials mode = %o, want %o", info.Mode().Perm(), credFilePerm)
	}
	got, err := ReadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != "https://new.example" {
		t.Errorf("normalized server = %q", got.Server)
	}
}

func TestWriteCredentialsRejectsSymlink(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	dir := filepath.Join(tmpDir, "hatchway")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmpDir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, credFileName)); err != nil {
		t.Fatal(err)
	}
	if err := WriteCredentials(&Credentials{Server: "https://api.example", Token: "secret"}); err == nil {
		t.Fatal("WriteCredentials() should reject a symlink target")
	}
}

func TestCredentialsRejectSymlinkDirectory(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	configRoot := filepath.Join(tmpDir, "config")
	target := filepath.Join(tmpDir, "target")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configRoot, "hatchway")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configRoot)

	cred := &Credentials{Server: "https://api.example", Token: "secret"}
	if err := WriteCredentials(cred); err == nil {
		t.Fatal("WriteCredentials() should reject a symlink config directory")
	}
	if _, err := ReadCredentials(); err == nil {
		t.Fatal("ReadCredentials() should reject a symlink config directory")
	}
}

func TestCredentialsRejectLooseDirectoryPermissions(t *testing.T) {
	clearCredentialEnv(t)
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	dir := filepath.Join(configRoot, "hatchway")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, credFileName), []byte(`{"server":"https://api.example","token":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCredentials(); err == nil {
		t.Fatal("ReadCredentials() should reject a group/world-accessible config directory")
	}
	if err := WriteCredentials(&Credentials{Server: "https://api.example", Token: "secret"}); err == nil {
		t.Fatal("WriteCredentials() should reject a group/world-accessible config directory")
	}
}

func TestReadCredentialsNoFile(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("ReadCredentials: %v", err)
	}
	if got != nil {
		t.Error("expected nil for missing file")
	}
}

func TestDeleteCredentials(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cred := &Credentials{Server: "https://example.com", Token: "test"}
	if err := WriteCredentials(cred); err != nil {
		t.Fatalf("WriteCredentials: %v", err)
	}

	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}

	got, err := ReadCredentials()
	if err != nil {
		t.Fatalf("ReadCredentials: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteCredentialsIdempotent(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Delete on non-existent file should not error
	if err := DeleteCredentials(); err != nil {
		t.Fatalf("DeleteCredentials on missing file: %v", err)
	}
}

func TestResolveCredentialsFromEnv(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_SERVER", "https://from-env.example.com")
	t.Setenv("HATCHWAY_TOKEN", "sk_live_envtoken")

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
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_SERVER", "https://override.example.com")
	t.Setenv("HATCHWAY_TOKEN", "sk_live_override")

	// Write file credentials
	if err := WriteCredentials(&Credentials{Server: "https://file.example.com", Token: "sk_live_file"}); err != nil {
		t.Fatalf("WriteCredentials: %v", err)
	}

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

func TestResolveCredentialsCompleteEnvOverridesBadFile(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_SERVER", "https://override.example.com/")
	t.Setenv("HATCHWAY_TOKEN", "sk_live_override")
	dir := filepath.Join(tmpDir, "hatchway")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, credFileName), []byte(`{not json}`), 0644); err != nil {
		t.Fatal(err)
	}

	cred, err := ResolveCredentials()
	if err != nil {
		t.Fatalf("ResolveCredentials() error = %v", err)
	}
	if cred.Server != "https://override.example.com" {
		t.Errorf("server = %q", cred.Server)
	}
}

func TestResolveCredentialsRejectsInvalidServerURL(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_SERVER", "api.example.com/path")
	t.Setenv("HATCHWAY_TOKEN", "sk_live_override")
	if _, err := ResolveCredentials(); err == nil {
		t.Fatal("ResolveCredentials() should reject a URL without a scheme")
	}
}

func TestResolveCredentialsNoAuth(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	_, err := ResolveCredentials()
	if err == nil {
		t.Error("expected error when not authenticated")
	}
}

func TestResolveCredentialsMissingServer(t *testing.T) {
	clearCredentialEnv(t)
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("HATCHWAY_TOKEN", "sk_live_test")

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

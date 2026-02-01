package gitauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/cbrown132/driftd/internal/config"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func writeKeyFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "id_rsa")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("open key file: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, block); err != nil {
		t.Fatalf("encode key: %v", err)
	}
	return path
}

func TestSSHAuthFromPath(t *testing.T) {
	keyPath := writeKeyFile(t)
	cfg := &config.GitAuthConfig{
		Type:                      "ssh",
		SSHKeyPath:                keyPath,
		SSHInsecureIgnoreHostKey: true,
	}

	auth, err := sshAuth(cfg)
	if err != nil {
		t.Fatalf("ssh auth: %v", err)
	}
	if auth == nil {
		t.Fatalf("expected auth")
	}
}

func TestSSHAuthFromEnv(t *testing.T) {
	keyPath := writeKeyFile(t)
	t.Setenv("SSH_KEY_PATH", keyPath)

	cfg := &config.GitAuthConfig{
		Type:                      "ssh",
		SSHKeyEnv:                 "SSH_KEY_PATH",
		SSHInsecureIgnoreHostKey: true,
	}

	auth, err := sshAuth(cfg)
	if err != nil {
		t.Fatalf("ssh auth: %v", err)
	}
	if auth == nil {
		t.Fatalf("expected auth")
	}
}

func TestSSHAuthRequiresKnownHosts(t *testing.T) {
	keyPath := writeKeyFile(t)
	cfg := &config.GitAuthConfig{
		Type:       "ssh",
		SSHKeyPath: keyPath,
	}

	if _, err := sshAuth(cfg); err == nil {
		t.Fatalf("expected error when known_hosts missing")
	}
}

func TestHTTPSAuthFromEnv(t *testing.T) {
	t.Setenv("GIT_TOKEN", "token123")
	cfg := &config.GitAuthConfig{
		Type:           "https",
		HTTPSTokenEnv:  "GIT_TOKEN",
		HTTPSUsername:  "bot",
	}

	auth, err := httpsAuth(cfg)
	if err != nil {
		t.Fatalf("https auth: %v", err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("expected BasicAuth")
	}
	if basic.Username != "bot" || basic.Password != "token123" {
		t.Fatalf("unexpected credentials: %#v", basic)
	}
}

func TestHTTPSAuthFallbackUsername(t *testing.T) {
	cfg := &config.GitAuthConfig{
		Type:        "https",
		HTTPSToken: "token123",
	}

	auth, err := httpsAuth(cfg)
	if err != nil {
		t.Fatalf("https auth: %v", err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("expected BasicAuth")
	}
	if basic.Username != "x-access-token" {
		t.Fatalf("expected fallback username, got %q", basic.Username)
	}
}

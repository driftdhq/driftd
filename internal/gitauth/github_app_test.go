package gitauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/driftdhq/driftd/internal/config"
)

func TestGitHubAppTokenCaching(t *testing.T) {
	clearTokenCache()

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"test-token"}`)
	}))
	defer server.Close()

	key := generateTestKey(t)

	cfg := &config.GitHubAppConfig{
		AppID:          1,
		InstallationID: 2,
		PrivateKey:     key,
		APIBaseURL:     server.URL,
	}

	ctx := context.Background()
	token1, err := GitHubAppToken(ctx, cfg)
	if err != nil {
		t.Fatalf("token1: %v", err)
	}
	token2, err := GitHubAppToken(ctx, cfg)
	if err != nil {
		t.Fatalf("token2: %v", err)
	}
	if token1 != token2 {
		t.Fatalf("expected cached token")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 request, got %d", hits)
	}
}

func generateTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

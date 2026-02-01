package gitauth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cbrown132/driftd/internal/config"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/golang-jwt/jwt/v5"
)

type appTokenCache struct {
	mu     sync.Mutex
	token  string
	expiry time.Time
}

var tokenCache sync.Map

func clearTokenCache() {
	tokenCache = sync.Map{}
}

func githubAppAuth(ctx context.Context, cfg *config.GitAuthConfig) (*githttp.BasicAuth, error) {
	if cfg.GitHubApp == nil {
		return nil, fmt.Errorf("github_app config required")
	}
	token, err := githubAppToken(ctx, cfg.GitHubApp)
	if err != nil {
		return nil, err
	}
	username := cfg.HTTPSUsername
	if username == "" {
		username = "x-access-token"
	}
	return &githttp.BasicAuth{
		Username: username,
		Password: token,
	}, nil
}

func githubAppToken(ctx context.Context, cfg *config.GitHubAppConfig) (string, error) {
	if cfg.AppID == 0 || cfg.InstallationID == 0 {
		return "", fmt.Errorf("github_app app_id and installation_id required")
	}

	cacheKey := fmt.Sprintf("%d:%d", cfg.AppID, cfg.InstallationID)
	if cached, ok := tokenCache.Load(cacheKey); ok {
		c, ok := cached.(*appTokenCache)
		if !ok {
			// Invalid cache entry, fetch new token
			tokenCache.Delete(cacheKey)
		} else {
			c.mu.Lock()
			if c.token != "" && time.Until(c.expiry) > 2*time.Minute {
				token := c.token
				c.mu.Unlock()
				return token, nil
			}
			c.mu.Unlock()
		}
	}

	key, err := loadPrivateKey(cfg)
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": cfg.AppID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}

	baseURL := cfg.APIBaseURL
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", baseURL, cfg.InstallationID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github app token request failed: %s", resp.Status)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("github app token missing in response")
	}

	expiry := time.Now().Add(58 * time.Minute)
	c := &appTokenCache{token: body.Token, expiry: expiry}
	tokenCache.Store(cacheKey, c)
	return body.Token, nil
}

func loadPrivateKey(cfg *config.GitHubAppConfig) (*rsa.PrivateKey, error) {
	var keyData string
	switch {
	case cfg.PrivateKey != "":
		keyData = cfg.PrivateKey
	case cfg.PrivateKeyEnv != "":
		keyData = os.Getenv(cfg.PrivateKeyEnv)
	case cfg.PrivateKeyPath != "":
		data, err := os.ReadFile(cfg.PrivateKeyPath)
		if err != nil {
			return nil, err
		}
		keyData = string(data)
	default:
		return nil, fmt.Errorf("github_app private key required")
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(keyData))
	if err != nil {
		return nil, fmt.Errorf("parse github app private key: %w", err)
	}
	return key, nil
}

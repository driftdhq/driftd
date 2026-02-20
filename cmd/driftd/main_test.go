package main

import (
	"testing"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

func TestValidateServeSecurity(t *testing.T) {
	t.Run("allows insecure dev mode", func(t *testing.T) {
		cfg := &config.Config{InsecureDevMode: true}
		if err := validateServeSecurity(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("fails without auth when secure mode", func(t *testing.T) {
		cfg := &config.Config{}
		if err := validateServeSecurity(cfg); err == nil {
			t.Fatalf("expected error when auth is not configured")
		}
	})

	t.Run("accepts api auth token", func(t *testing.T) {
		cfg := &config.Config{
			APIAuth: config.APIAuthConfig{
				Token: "read-token",
			},
		}
		if err := validateServeSecurity(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("accepts external auth mode without internal auth", func(t *testing.T) {
		cfg := &config.Config{
			Auth: config.AuthConfig{
				Mode: "external",
				External: config.ExternalAuthConfig{
					UserHeader: "X-Auth-Request-User",
				},
			},
		}
		if err := validateServeSecurity(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})
}

func TestValidateEncryptionKeyPolicy(t *testing.T) {
	t.Run("allows insecure dev mode", func(t *testing.T) {
		t.Setenv(secrets.EnvEncryptionKey, "")
		cfg := &config.Config{InsecureDevMode: true}
		if err := validateEncryptionKeyPolicy(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("requires env key in secure mode", func(t *testing.T) {
		t.Setenv(secrets.EnvEncryptionKey, "")
		cfg := &config.Config{}
		if err := validateEncryptionKeyPolicy(cfg); err == nil {
			t.Fatalf("expected error without %s", secrets.EnvEncryptionKey)
		}
	})

	t.Run("accepts env key in secure mode", func(t *testing.T) {
		t.Setenv(secrets.EnvEncryptionKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		cfg := &config.Config{}
		if err := validateEncryptionKeyPolicy(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("rejects malformed env key in secure mode", func(t *testing.T) {
		t.Setenv(secrets.EnvEncryptionKey, "not-a-valid-key")
		cfg := &config.Config{}
		if err := validateEncryptionKeyPolicy(cfg); err == nil {
			t.Fatalf("expected error for malformed %s", secrets.EnvEncryptionKey)
		}
	})

	t.Run("rejects malformed env key in insecure mode", func(t *testing.T) {
		t.Setenv(secrets.EnvEncryptionKey, "not-a-valid-key")
		cfg := &config.Config{InsecureDevMode: true}
		if err := validateEncryptionKeyPolicy(cfg); err == nil {
			t.Fatalf("expected error for malformed %s", secrets.EnvEncryptionKey)
		}
	})
}

func TestValidateInsecureDevModeBind(t *testing.T) {
	t.Run("allows secure mode regardless of listen addr", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: false,
			ListenAddr:      ":8080",
		}
		if err := validateInsecureDevModeBind(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("allows insecure mode on localhost hostname", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "localhost:8080",
		}
		if err := validateInsecureDevModeBind(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("allows insecure mode on loopback ip", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "127.0.0.1:8080",
		}
		if err := validateInsecureDevModeBind(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("allows insecure mode on ipv6 loopback", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "[::1]:8080",
		}
		if err := validateInsecureDevModeBind(cfg); err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("rejects insecure mode on wildcard bind", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      ":8080",
		}
		if err := validateInsecureDevModeBind(cfg); err == nil {
			t.Fatal("expected error for non-local listen addr")
		}
	})

	t.Run("rejects insecure mode on non-local bind", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "0.0.0.0:8080",
		}
		if err := validateInsecureDevModeBind(cfg); err == nil {
			t.Fatal("expected error for non-local listen addr")
		}
	})

	t.Run("rejects insecure mode on empty listen addr", func(t *testing.T) {
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "",
		}
		if err := validateInsecureDevModeBind(cfg); err == nil {
			t.Fatal("expected error for empty listen addr")
		}
	})

	t.Run("allows explicit override", func(t *testing.T) {
		t.Setenv(envAllowInsecureDevNonLocal, "true")
		cfg := &config.Config{
			InsecureDevMode: true,
			ListenAddr:      "0.0.0.0:8080",
		}
		if err := validateInsecureDevModeBind(cfg); err != nil {
			t.Fatalf("expected nil error with override, got %v", err)
		}
	})
}

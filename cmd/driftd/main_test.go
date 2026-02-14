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
}

func TestValidateEncryptionKeyPolicy(t *testing.T) {
	t.Run("allows insecure dev mode", func(t *testing.T) {
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
}

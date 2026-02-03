package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvEncryptionKey is the environment variable name for the encryption key.
	EnvEncryptionKey = "DRIFTD_ENCRYPTION_KEY"

	// KeyFileName is the default filename for storing the encryption key.
	KeyFileName = ".encryption-key"
)

// KeyStore manages encryption key loading and generation.
type KeyStore struct {
	dataDir string
}

// NewKeyStore creates a new KeyStore that stores keys in the given data directory.
func NewKeyStore(dataDir string) *KeyStore {
	return &KeyStore{dataDir: dataDir}
}

// LoadOrGenerate loads the encryption key from available sources, or generates
// a new one if none exists. Key sources are checked in order:
// 1. DRIFTD_ENCRYPTION_KEY environment variable
// 2. Key file in data directory
// 3. Generate new key and save to file
func (ks *KeyStore) LoadOrGenerate() ([]byte, error) {
	// 1. Check environment variable
	if encoded := os.Getenv(EnvEncryptionKey); encoded != "" {
		key, err := DecodeKey(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", EnvEncryptionKey, err)
		}
		return key, nil
	}

	// 2. Check key file
	keyPath := ks.keyFilePath()
	if data, err := os.ReadFile(keyPath); err == nil {
		key, err := DecodeKey(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("invalid key file %s: %w", keyPath, err)
		}
		return key, nil
	}

	// 3. Generate new key and save
	key, err := GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate encryption key: %w", err)
	}

	if err := ks.saveKey(key); err != nil {
		return nil, fmt.Errorf("failed to save encryption key: %w", err)
	}

	return key, nil
}

// keyFilePath returns the path to the key file.
func (ks *KeyStore) keyFilePath() string {
	return filepath.Join(ks.dataDir, KeyFileName)
}

// saveKey saves the key to the key file with restricted permissions.
func (ks *KeyStore) saveKey(key []byte) error {
	keyPath := ks.keyFilePath()

	// Ensure data directory exists
	if err := os.MkdirAll(ks.dataDir, 0750); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Write key with restricted permissions (owner read/write only)
	encoded := EncodeKey(key)
	if err := os.WriteFile(keyPath, []byte(encoded+"\n"), 0600); err != nil {
		return fmt.Errorf("failed to write key file: %w", err)
	}

	return nil
}

// KeyExists returns true if an encryption key is available from any source.
func (ks *KeyStore) KeyExists() bool {
	if os.Getenv(EnvEncryptionKey) != "" {
		return true
	}
	if _, err := os.Stat(ks.keyFilePath()); err == nil {
		return true
	}
	return false
}

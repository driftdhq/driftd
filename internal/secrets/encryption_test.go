package secrets

import (
	"bytes"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	if len(key) != KeySize {
		t.Errorf("GenerateKey() key length = %d, want %d", len(key), KeySize)
	}

	// Ensure keys are unique
	key2, _ := GenerateKey()
	if bytes.Equal(key, key2) {
		t.Error("GenerateKey() generated identical keys")
	}
}

func TestEncodeDecodeKey(t *testing.T) {
	key, _ := GenerateKey()
	encoded := EncodeKey(key)
	decoded, err := DecodeKey(encoded)
	if err != nil {
		t.Fatalf("DecodeKey() error = %v", err)
	}
	if !bytes.Equal(key, decoded) {
		t.Error("DecodeKey() did not return original key")
	}
}

func TestDecodeKey_InvalidSize(t *testing.T) {
	// 16-byte key (too short)
	shortKey := "dGhpcyBpcyBvbmx5IDE2Yg=="
	_, err := DecodeKey(shortKey)
	if err != ErrInvalidKeySize {
		t.Errorf("DecodeKey() error = %v, want %v", err, ErrInvalidKeySize)
	}
}

func TestNewEncryptor_InvalidKeySize(t *testing.T) {
	shortKey := make([]byte, 16)
	_, err := NewEncryptor(shortKey)
	if err != ErrInvalidKeySize {
		t.Errorf("NewEncryptor() error = %v, want %v", err, ErrInvalidKeySize)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key, _ := GenerateKey()
	enc, err := NewEncryptor(key)
	if err != nil {
		t.Fatalf("NewEncryptor() error = %v", err)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{"empty", ""},
		{"short", "hello"},
		{"long", "this is a much longer string with special chars: !@#$%^&*()"},
		{"unicode", "hello 世界 🔐"},
		{"multiline", "line1\nline2\nline3"},
		{"pem-like", "-----BEGIN RSA PRIVATE KEY-----\nMIIE....\n-----END RSA PRIVATE KEY-----"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ciphertext, err := enc.EncryptString(tt.plaintext)
			if err != nil {
				t.Fatalf("EncryptString() error = %v", err)
			}

			// Ciphertext should be different from plaintext
			if ciphertext == tt.plaintext && tt.plaintext != "" {
				t.Error("EncryptString() ciphertext equals plaintext")
			}

			decrypted, err := enc.DecryptString(ciphertext)
			if err != nil {
				t.Fatalf("DecryptString() error = %v", err)
			}

			if decrypted != tt.plaintext {
				t.Errorf("DecryptString() = %q, want %q", decrypted, tt.plaintext)
			}
		})
	}
}

func TestEncrypt_UniqueNonce(t *testing.T) {
	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	plaintext := "same plaintext"
	ciphertext1, _ := enc.EncryptString(plaintext)
	ciphertext2, _ := enc.EncryptString(plaintext)

	// Same plaintext should produce different ciphertext due to random nonce
	if ciphertext1 == ciphertext2 {
		t.Error("Encrypt() produced identical ciphertext for same plaintext")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1, _ := GenerateKey()
	key2, _ := GenerateKey()
	enc1, _ := NewEncryptor(key1)
	enc2, _ := NewEncryptor(key2)

	ciphertext, _ := enc1.EncryptString("secret")
	_, err := enc2.DecryptString(ciphertext)
	if err != ErrDecryptionFailed {
		t.Errorf("DecryptString() with wrong key error = %v, want %v", err, ErrDecryptionFailed)
	}
}

func TestDecrypt_InvalidCiphertext(t *testing.T) {
	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	tests := []struct {
		name       string
		ciphertext string
		wantErr    error
	}{
		{"invalid base64", "not-valid-base64!", nil}, // base64 decode error
		{"too short", "YWJj", ErrInvalidCiphertext},  // valid base64 but too short
		{"tampered", "", nil},                        // will test with modified ciphertext
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := enc.DecryptString(tt.ciphertext)
			if err == nil {
				t.Error("DecryptString() expected error, got nil")
			}
		})
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	key, _ := GenerateKey()
	enc, _ := NewEncryptor(key)

	ciphertext, _ := enc.EncryptString("secret")
	// Tamper with the ciphertext by changing a character
	tampered := []byte(ciphertext)
	tampered[len(tampered)/2] ^= 0xFF

	_, err := enc.DecryptString(string(tampered))
	if err == nil {
		t.Error("DecryptString() with tampered ciphertext should fail")
	}
}

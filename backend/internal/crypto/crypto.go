// Package crypto 提供凭证信封加密与 API Key 哈希。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

var ErrCiphertextTooShort = errors.New("ciphertext too short")

// Encrypt AES-256-GCM。返回 base64(nonce||ciphertext||tag)。
func Encrypt(key, plaintext []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("AES-256 需要 32 字节 key，当前 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密 Encrypt 的输出。
func Decrypt(key []byte, encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return nil, ErrCiphertextTooShort
	}
	return aead.Open(nil, raw[:ns], raw[ns:], nil)
}

// HashAPIKey 返回 API Key 的 SHA-256 十六进制（不存明文）。
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIKey 生成 sk-iso-<random> 并给出前缀与哈希。
func GenerateAPIKey() (full, prefix, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, buf); err != nil {
		return
	}
	full = "sk-iso-" + base64.RawURLEncoding.EncodeToString(buf)
	prefix = full[:12]
	hash = HashAPIKey(full)
	return
}

// HashPassword 使用 argon2id。格式: argon2id$salt$hash（base64）。
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := argon2IDKey([]byte(password), salt)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

// VerifyPassword 校验 argon2id 密码。
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := argon2IDKey([]byte(password), salt)
	return hmac.Equal(got, want)
}

func argon2IDKey(password, salt []byte) []byte {
	// OWASP 推荐基线：argon2id, t=3, m=64MB, p=4, 32字节输出。
	return argon2.IDKey(password, salt, 3, 64*1024, 4, 32)
}

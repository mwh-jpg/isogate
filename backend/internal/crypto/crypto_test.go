package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	plain := []byte(`{"api_key":"sk-ant-secret","proxy_pass":"p@ss"}`)
	enc, err := Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(enc), []byte("sk-ant")) {
		t.Fatal("密文泄露明文")
	}
	got, err := Decrypt(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密不一致: %s", got)
	}
}

func TestEncryptWrongKey(t *testing.T) {
	enc, _ := Encrypt(bytes.Repeat([]byte("a"), 32), []byte("secret"))
	if _, err := Decrypt(bytes.Repeat([]byte("b"), 32), enc); err == nil {
		t.Fatal("错误密钥应解密失败")
	}
}

func TestEncryptBadKeyLength(t *testing.T) {
	if _, err := Encrypt([]byte("short"), []byte("x")); err == nil {
		t.Fatal("非32字节key应失败")
	}
}

func TestGenerateAPIKey(t *testing.T) {
	full, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(full, "sk-iso-") || prefix != full[:12] || hash != HashAPIKey(full) {
		t.Fatalf("key格式错误: %s %s %s", full, prefix, hash)
	}
}

func TestPassword(t *testing.T) {
	stored, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", stored) {
		t.Fatal("正确密码应通过")
	}
	if VerifyPassword("wrong", stored) {
		t.Fatal("错误密码应拒绝")
	}
	if !strings.HasPrefix(stored, "argon2id$") {
		t.Fatal("应使用argon2id")
	}
}

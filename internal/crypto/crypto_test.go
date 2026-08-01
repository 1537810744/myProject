package crypto

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEncryptDecrypt 加解密往返：密文 ≠ 明文，解密能还原。
func TestEncryptDecrypt(t *testing.T) {
	k := &Keyring{key: make([]byte, keyLen)} // 全零密钥，测功能不测密码强度
	plain := "my-secret-abc-123"

	enc, err := k.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt 失败: %v", err)
	}
	if enc == plain {
		t.Fatal("密文不应等于明文")
	}
	if enc == "" {
		t.Fatal("密文为空")
	}

	got, err := k.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt 失败: %v", err)
	}
	if got != plain {
		t.Fatalf("解密结果不一致: got %q want %q", got, plain)
	}
}

// TestEncryptIsRandom 同一明文加密两次，密文必须不同（随机 nonce）。
func TestEncryptIsRandom(t *testing.T) {
	k := &Keyring{key: make([]byte, keyLen)}
	a, _ := k.Encrypt("same")
	b, _ := k.Encrypt("same")
	if a == b {
		t.Fatal("同一明文两次加密密文应不同（保证不可预测）")
	}
}

// TestDecryptWrongKey 用错误密钥解密必须失败。
func TestDecryptWrongKey(t *testing.T) {
	k1 := &Keyring{key: make([]byte, keyLen)}
	k2 := &Keyring{key: append(make([]byte, keyLen-1), 1)} // 不同密钥
	enc, err := k1.Encrypt("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k2.Decrypt(enc); err == nil {
		t.Fatal("错误密钥解密应当失败")
	}
}

// TestDecryptOrRaw 解密失败时回退明文（兼容旧数据）。
func TestDecryptOrRaw(t *testing.T) {
	k := &Keyring{key: make([]byte, keyLen)}
	if got := k.DecryptOrRaw("legacy-plaintext"); got != "legacy-plaintext" {
		t.Fatalf("明文回退失败: got %q", got)
	}
	enc, _ := k.Encrypt("encrypted-value")
	if got := k.DecryptOrRaw(enc); got != "encrypted-value" {
		t.Fatalf("加密值解密失败: got %q", got)
	}
}

// TestNewKeyringWithEnv 用 MASTER_KEY 环境变量创建。
func TestNewKeyringWithEnv(t *testing.T) {
	t.Setenv(keyEnv, "some-master-secret")
	k, err := NewKeyring(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(k.key) != keyLen {
		t.Fatalf("派生密钥长度应为 %d，got %d", keyLen, len(k.key))
	}
}

// TestNewKeyringCreatesFile 无环境变量时自动生成密钥文件且权限 0600。
func TestNewKeyringCreatesFile(t *testing.T) {
	t.Setenv(keyEnv, "") // 清掉环境变量
	dir := t.TempDir()
	k, err := NewKeyring(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, keyFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密钥文件未生成: %v", err)
	}
	// 0600 权限是 Unix 概念，Windows 的 os.WriteFile 不保证它，只在类 Unix 上断言。
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 0600，got %v", info.Mode().Perm())
	}
	if len(k.key) != keyLen {
		t.Fatalf("密钥长度应为 %d", keyLen)
	}
	// 再次创建应复用已有文件（不覆盖）
	k2, err := NewKeyring(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(k.key) != string(k2.key) {
		t.Fatal("两次创建的密钥应一致（复用文件）")
	}
}

// TestNewKeyringPersists 用文件密钥加密 → 重新加载密钥 → 能解密（跨重启可用）。
func TestNewKeyringPersists(t *testing.T) {
	t.Setenv(keyEnv, "")
	dir := t.TempDir()
	k1, err := NewKeyring(dir)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := k1.Encrypt("persist-me")
	k2, err := NewKeyring(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := k2.Decrypt(enc)
	if err != nil || got != "persist-me" {
		t.Fatalf("跨重启解密失败: got %q err %v", got, err)
	}
}

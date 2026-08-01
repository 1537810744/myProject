// 【安全】凭证加密包。
// 职责：用 AES-256-GCM 对 API Secret 做"静态加密"(encryption at rest)，
// 保证数据库文件泄露时 Secret 不直接裸露。
// Key 来源（二选一）：
//
//	a) 环境变量 MASTER_KEY —— 更安全，部署时注入（推荐）；
//	b) 自动生成文件 <数据目录>/master.key —— 首次运行生成，权限 0600（仅当前用户可读写），
//	   作为本机免配置的兜底。
//
// 语法点：package、const/var、[]byte 字节切片、crypto/aes 分组加密、crypto/cipher 认证加密、
// crypto/rand 随机数、crypto/sha256 哈希、encoding/base64、errors.Is、文件权限。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// keyEnv 环境变量名：自定义主密钥（推荐优先使用）。
const keyEnv = "MASTER_KEY"

// keyFile 在数据目录里自动生成的密钥文件名（当 MASTER_KEY 未设置时的兜底）。
const keyFile = "master.key"

// keyLen AES-256 要求 32 字节密钥。
const keyLen = 32

// Keyring 密钥环：持有 AES 密钥，提供加解密。
// 为什么设计成结构体而不是包级函数？—— 便于注入和测试：main 创建一次，
// 传给 apiconfig（加密）和 exchange.Hub（解密），依赖关系明确。
type Keyring struct {
	key []byte // []byte 字节切片：AES 密钥（32 字节）
}

// NewKeyring 创建密钥环：优先读 MASTER_KEY 环境变量，否则在 dir 下生成 master.key。
func NewKeyring(dir string) (*Keyring, error) {
	if mk := os.Getenv(keyEnv); mk != "" { // 环境变量优先
		// 环境变量可以是任意长度字符串，用 SHA-256 派生固定 32 字节密钥。
		return &Keyring{key: deriveKey([]byte(mk))}, nil
	}
	path := filepath.Join(dir, keyFile) // 数据目录下的密钥文件路径
	key, err := loadOrCreateFileKey(path)
	if err != nil {
		return nil, err
	}
	return &Keyring{key: key}, nil
}

// deriveKey 用 SHA-256 把任意长度的主密钥字符串哈希成固定 32 字节。
func deriveKey(master []byte) []byte {
	sum := sha256.Sum256(master) // crypto/sha256：输出固定 32 字节
	return sum[:]                // 切片取全部 32 字节
}

// loadOrCreateFileKey 读取或创建密钥文件（权限 0600）。
func loadOrCreateFileKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil { // 文件已存在，直接读
		if len(data) == keyLen {
			return data, nil
		}
		return nil, fmt.Errorf("密钥文件 %s 长度异常(%d 字节)，期望 %d", path, len(data), keyLen)
	} else if !errors.Is(err, os.ErrNotExist) { // 读失败且不是"文件不存在"
		return nil, fmt.Errorf("读取密钥文件失败: %w", err)
	}
	// 文件不存在 → 生成一个随机密钥并写入（0600：仅当前用户可读写）。
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil { // 从加密随机源读满 32 字节
		return nil, fmt.Errorf("生成随机密钥失败: %w", err)
	}
	// os.WriteFile(路径, 内容, 权限)。0o600 八进制 = 用户可读写，组/其他不可访问。
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("写入密钥文件 %s 失败: %w", path, err)
	}
	return key, nil
}

// Encrypt 加密明文，返回 base64 字符串（格式：随机nonce + 密文）。
func (k *Keyring) Encrypt(plain string) (string, error) {
	block, err := aes.NewCipher(k.key) // 用密钥创建 AES 分组密码
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block) // GCM 认证加密模式：加密 + 防篡改
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize()) // 随机 nonce（每次加密都不同，即使明文相同）
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	// gcm.Seal：加密。把 nonce 拼在密文前面一起返回（解密时要用同一个 nonce）。
	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 解密 base64 密文，返回明文。失败返回错误。
func (k *Keyring) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded) // base64 还原字节
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() { // 数据长度非法（连 nonce 都不够）
		return "", errors.New("密文长度异常")
	}
	nonce := data[:gcm.NonceSize()]             // 前 N 字节是 nonce
	ct := data[gcm.NonceSize():]                // 剩下的是密文
	plain, err := gcm.Open(nil, nonce, ct, nil) // 解密 + 校验完整性
	if err != nil {
		return "", err // 密钥不对或密文被篡改都会在这报错
	}
	return string(plain), nil
}

// DecryptOrRaw 解密；失败时把原文当明文返回（兼容历史未加密数据）。
// 用途：数据库里可能既有加密后的 Secret（新写入），也有历史明文（旧版本），
// 解密失败就按明文用，保证升级不破坏老数据。新数据一律走 Encrypt。
func (k *Keyring) DecryptOrRaw(stored string) string {
	if stored == "" {
		return ""
	}
	if plain, err := k.Decrypt(stored); err == nil {
		return plain
	}
	return stored // 解不开 → 当作明文（旧数据）
}

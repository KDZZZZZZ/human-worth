package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// 【阅读 1】KeyRing 保存当前密钥 ID 与轮换期间仍可读取的旧密钥；每个值编码 32 字节密钥。
// cmd/identity 分别加载加密和签名密钥环：前者保护数据库中的可恢复秘密，
// 后者供 adapter/security/jwt.go 签发和验证 ActorAssertion；密文或 JWT 中的 kid 用于选择轮换前后的密钥。
type KeyRing struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"`
}

// 【阅读 2】LoadKeyRing 读取并验证整个密钥环，确保密钥 ID、长度及当前活动密钥均有效。
func LoadKeyRing(path string) (KeyRing, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return KeyRing{}, errors.New("key file unavailable")
	}
	var ring KeyRing
	if json.Unmarshal(data, &ring) != nil || ring.Active == "" || len(ring.Keys) == 0 {
		return ring, errors.New("invalid key ring")
	}
	for id := range ring.Keys {
		if id == "" || strings.Contains(id, ".") {
			return ring, errors.New("invalid key id")
		}
		if _, err = ring.key(id); err != nil {
			return ring, err
		}
	}
	_, err = ring.key(ring.Active)
	return ring, err
}

// 【阅读 3】key 按 ID 解码固定 32 字节的密钥；未知 ID 或错误编码都直接拒绝。
func (k KeyRing) key(id string) ([]byte, error) {
	value, ok := k.Keys[id]
	if !ok {
		return nil, errors.New("unknown key")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("key must contain 32 random bytes")
	}
	return decoded, nil
}

// 【阅读 4】RandomToken 生成 256 位随机、可用于 URL 的不透明令牌；随机源失效时不降级生成凭据。
func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// 【阅读 5】NewID 为随机标识添加对象类型前缀，供账号、凭据、登录流程及审计记录使用。
func NewID(prefix string) string { return prefix + "_" + RandomToken() }

// 【阅读 6】Digest 计算令牌的 SHA-256，用于数据库查找或比较，避免保存原始 Cookie、state、nonce 和 MCP token。
func Digest(text string) []byte { sum := sha256.Sum256([]byte(text)); return sum[:] }

// 【阅读 7】Seal 用当前密钥和随机 nonce 执行 AES-GCM 加密，输出 kid 与包含 nonce 的密文。
// purpose 作为附加认证数据绑定对象及用途：login.go 使用 flowID:verifier，
// 会话使用 credentialID:csrf；复制密文到其他对象或用途后无法通过 Open 验证。
func (k KeyRing) Seal(value, purpose string) (string, error) {
	key, err := k.key(k.Active)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	encrypted := gcm.Seal(nonce, nonce, []byte(value), []byte(purpose))
	return k.Active + "." + base64.RawURLEncoding.EncodeToString(encrypted), nil
}

// 【阅读 8】Open 按密文中的 kid 选择密钥并校验用途，供登录换码与会话 CSRF 校验恢复明文。
// 旧密钥保留期间可解密旧记录；移除旧密钥后对应密文失效，不回退尝试其他密钥。
func (k KeyRing) Open(value, purpose string) (string, error) {
	id, value, found := strings.Cut(value, ".")
	if !found {
		return "", errors.New("invalid ciphertext")
	}
	key, err := k.key(id)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	encrypted, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(encrypted) < gcm.NonceSize() {
		return "", errors.New("invalid ciphertext")
	}
	plain, err := gcm.Open(nil, encrypted[:gcm.NonceSize()], encrypted[gcm.NonceSize():], []byte(purpose))
	return string(plain), err
}

func (k KeyRing) Validate() error { _, err := k.key(k.Active); return err }

type Secrets struct{ encryption KeyRing }

func NewSecrets(ring KeyRing) (*Secrets, error) {
	if err := ring.Validate(); err != nil {
		return nil, err
	}
	return &Secrets{encryption: ring}, nil
}
func (*Secrets) RandomToken() string        { return RandomToken() }
func (*Secrets) Digest(value string) []byte { return Digest(value) }
func (s *Secrets) Seal(value, purpose string) (string, error) {
	return s.encryption.Seal(value, purpose)
}
func (s *Secrets) Open(value, purpose string) (string, error) {
	return s.encryption.Open(value, purpose)
}

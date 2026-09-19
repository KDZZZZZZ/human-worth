package identity

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

type KeyRing struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"`
}

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
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func newID(prefix string) string { return prefix + "_" + randomToken() }
func digest(text string) []byte  { sum := sha256.Sum256([]byte(text)); return sum[:] }
func (k KeyRing) seal(value, purpose string) (string, error) {
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
func (k KeyRing) open(value, purpose string) (string, error) {
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

package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"github.com/cockroachdb/errors"
)

// ErrDecrypt 表示密文无法解密：不是 Encrypt 的输出、被改过，或者加密它的密钥已经不在配置中。
var ErrDecrypt = errors.New("crypt: cannot decrypt")

const (
	keyLen = 32
	// version 是密文的第一个字节，以后更换格式时用来区分。
	version byte = 1
)

var b64 = base64.RawURLEncoding

// Config 是加密配置，对应配置文件的 crypt 段。
type Config struct {
	// Key 是应用密钥：32 字节随机数的 base64，用 openssl rand -base64 32 生成。
	Key string `yaml:"key"`
	// PreviousKeys 是轮换前的旧密钥，只用于解密和验签。
	PreviousKeys []string `yaml:"previous_keys"`
}

// Validate 检查 key 已填写，key 与 previous_keys 都是 32 字节的 base64。
func (c Config) Validate() error {
	var errs []error
	if c.Key == "" {
		errs = append(errs, errors.New("key is required; generate one with: openssl rand -base64 32"))
	} else if _, err := decodeKey(c.Key); err != nil {
		errs = append(errs, errors.Wrap(err, "key"))
	}
	for i, k := range c.PreviousKeys {
		if _, err := decodeKey(k); err != nil {
			errs = append(errs, errors.Wrapf(err, "previous_keys[%d]", i))
		}
	}
	return errors.Join(errs...)
}

func decodeKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("must be base64")
	}
	if len(key) != keyLen {
		return nil, errors.Newf("must decode to %d bytes, got %d", keyLen, len(key))
	}
	return key, nil
}

// GenerateKey 返回一个新的随机密钥，格式与 Config.Key 相同。
func GenerateKey() string {
	key := make([]byte, keyLen)
	rand.Read(key)
	return base64.StdEncoding.EncodeToString(key)
}

// Crypt 加解密数据、签名和校验链接，可以并发使用。
type Crypt struct {
	// aeads 与 macKeys 的第一项来自当前密钥，其余来自 PreviousKeys。
	aeads   []cipher.AEAD
	macKeys [][]byte
}

// New 创建 Crypt。cfg 无效时 panic。
func New(cfg Config) *Crypt {
	if err := cfg.Validate(); err != nil {
		panic("crypt: " + err.Error())
	}
	c := &Crypt{}
	for _, s := range append([]string{cfg.Key}, cfg.PreviousKeys...) {
		master, _ := decodeKey(s)
		block, err := aes.NewCipher(derive(master, "mygoravel crypt encrypt v1"))
		if err != nil {
			panic("crypt: " + err.Error())
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			panic("crypt: " + err.Error())
		}
		c.aeads = append(c.aeads, aead)
		c.macKeys = append(c.macKeys, derive(master, "mygoravel crypt sign v1"))
	}
	return c
}

// derive 用 HKDF-SHA256 从应用密钥派生一把用途专一的子密钥。
func derive(master []byte, info string) []byte {
	key, err := hkdf.Key(sha256.New, master, nil, info, keyLen)
	if err != nil {
		panic("crypt: " + err.Error())
	}
	return key
}

// Encrypt 用当前密钥加密 plaintext，返回 URL 安全的 base64 文本。nonce 每次随机生成，
// 同一个明文两次的结果不同。
func (c *Crypt) Encrypt(plaintext string) string {
	return b64.EncodeToString(c.aeads[0].Seal([]byte{version}, nil, []byte(plaintext), nil))
}

// Decrypt 解密 Encrypt 的输出，依次尝试当前密钥和 PreviousKeys。无法解密时返回 ErrDecrypt。
func (c *Crypt) Decrypt(ciphertext string) (string, error) {
	data, err := b64.DecodeString(ciphertext)
	if err != nil || len(data) == 0 || data[0] != version {
		return "", ErrDecrypt
	}
	for _, aead := range c.aeads {
		if plain, err := aead.Open(nil, nil, data[1:], nil); err == nil {
			return string(plain), nil
		}
	}
	return "", ErrDecrypt
}

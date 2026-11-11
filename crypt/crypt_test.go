package crypt_test

import (
	"encoding/base64"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/crypt"
)

func newCrypt(t *testing.T, key string, previous ...string) *crypt.Crypt {
	t.Helper()
	return crypt.New(crypt.Config{Key: key, PreviousKeys: previous})
}

func TestEncryptRoundTrip(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	for _, plain := range []string{"", "sk-test-123", "中文与\x00二进制", strings.Repeat("x", 1<<16)} {
		enc := c.Encrypt(plain)
		got, err := c.Decrypt(enc)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
}

func TestEncryptIsRandomizedAndURLSafe(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	a, b := c.Encrypt("secret"), c.Encrypt("secret")
	assert.NotEqual(t, a, b)
	assert.NotContains(t, a, "secret")
	assert.NotContainsf(t, a, "+", "ciphertext must be URL safe")
	assert.NotContains(t, a, "/")
	assert.NotContains(t, a, "=")
}

func TestDecryptRejectsTamperedOrForeign(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	enc := c.Encrypt("secret")
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	require.NoError(t, err)

	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)-1] ^= 1
	badVersion := append([]byte(nil), raw...)
	badVersion[0] = 9

	for name, s := range map[string]string{
		"empty":       "",
		"not base64":  "%%%",
		"truncated":   enc[:10],
		"flipped":     base64.RawURLEncoding.EncodeToString(flipped),
		"bad version": base64.RawURLEncoding.EncodeToString(badVersion),
		"other key":   newCrypt(t, crypt.GenerateKey()).Encrypt("secret"),
	} {
		_, err := c.Decrypt(s)
		assert.ErrorIs(t, err, crypt.ErrDecrypt, name)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := crypt.GenerateKey(), crypt.GenerateKey()
	before := newCrypt(t, oldKey)
	after := newCrypt(t, newKey, oldKey)

	got, err := after.Decrypt(before.Encrypt("old data"))
	require.NoError(t, err)
	assert.Equal(t, "old data", got)

	// 新数据只用当前密钥加密，去掉旧密钥之后照样能解。
	fresh := after.Encrypt("new data")
	got, err = newCrypt(t, newKey).Decrypt(fresh)
	require.NoError(t, err)
	assert.Equal(t, "new data", got)
	_, err = before.Decrypt(fresh)
	assert.ErrorIs(t, err, crypt.ErrDecrypt)
}

func TestConcurrentUse(t *testing.T) {
	c := newCrypt(t, crypt.GenerateKey())
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				got, err := c.Decrypt(c.Encrypt("v"))
				assert.NoError(t, err)
				assert.Equal(t, "v", got)
			}
		})
	}
	wg.Wait()
}

func TestConfigValidate(t *testing.T) {
	key := crypt.GenerateKey()
	assert.NoError(t, crypt.Config{Key: key, PreviousKeys: []string{crypt.GenerateKey()}}.Validate())

	err := crypt.Config{}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openssl rand -base64 32")

	err = crypt.Config{Key: "not base64!"}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key: must be base64")

	err = crypt.Config{Key: base64.StdEncoding.EncodeToString(make([]byte, 16))}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 bytes, got 16")

	err = crypt.Config{Key: key, PreviousKeys: []string{key, "short"}}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous_keys[1]")

	assert.PanicsWithValue(t, "crypt: key is required; generate one with: openssl rand -base64 32", func() {
		crypt.New(crypt.Config{})
	})
}

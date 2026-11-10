package hashing_test

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ryan053857/mygoravel/hashing"
)

// cheap 返回参数最小的配置，让测试保持快速。
func cheap(driver string) hashing.Config {
	return hashing.Config{
		Driver:   driver,
		Argon2id: hashing.Argon2idConfig{Memory: 8, Time: 1, Threads: 1},
		Bcrypt:   hashing.BcryptConfig{Cost: bcrypt.MinCost},
	}
}

func TestMakeAndCheck(t *testing.T) {
	for _, driver := range []string{"argon2id", "bcrypt"} {
		t.Run(driver, func(t *testing.T) {
			h := hashing.New(cheap(driver))
			hash, err := h.Make("secret")
			if err != nil {
				t.Fatal(err)
			}
			if !h.Check("secret", hash) {
				t.Fatal("correct password rejected")
			}
			if h.Check("Secret", hash) || h.Check("", hash) {
				t.Fatal("wrong password accepted")
			}
			again, _ := h.Make("secret")
			if again == hash {
				t.Fatal("hashes are not salted")
			}
			if h.NeedsRehash(hash) {
				t.Fatal("fresh hash needs rehash")
			}
		})
	}
}

func TestArgon2idFormat(t *testing.T) {
	hash, _ := hashing.New(cheap("argon2id")).Make("secret")
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("hash=%q", hash)
	}
}

func TestCheckAcceptsBothFormats(t *testing.T) {
	argon, _ := hashing.New(cheap("argon2id")).Make("password")
	bc, _ := hashing.New(cheap("bcrypt")).Make("password")
	// PHP password_hash 生成的 $2y$ 哈希，Laravel 用户工厂的默认密码 password。
	php := "$2y$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi"
	for _, driver := range []string{"argon2id", "bcrypt"} {
		h := hashing.New(cheap(driver))
		for _, hash := range []string{argon, bc, php} {
			if !h.Check("password", hash) {
				t.Errorf("driver %s rejected %q", driver, hash)
			}
		}
	}
}

func TestCheckRejectsMalformed(t *testing.T) {
	h := hashing.New(cheap("argon2id"))
	good, _ := h.Make("secret")
	for _, hash := range []string{
		"", "secret", "$argon2i$v=19$m=8,t=1,p=1$c2FsdA$a2V5",
		strings.Replace(good, "v=19", "v=16", 1),
		strings.Replace(good, "p=1$", "p=1x$", 1),
		strings.Replace(good, "t=1", "t=0", 1),
		good + "!", good[:strings.LastIndex(good, "$")+1],
	} {
		if h.Check("secret", hash) {
			t.Errorf("accepted %q", hash)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	low := hashing.New(cheap("argon2id"))
	argon, _ := low.Make("secret")
	bc, _ := hashing.New(cheap("bcrypt")).Make("secret")

	higher := cheap("argon2id")
	higher.Argon2id.Time = 2
	if !hashing.New(higher).NeedsRehash(argon) {
		t.Error("argon2id params changed")
	}
	if !low.NeedsRehash(bc) {
		t.Error("bcrypt hash under argon2id driver")
	}

	costlier := cheap("bcrypt")
	costlier.Bcrypt.Cost++
	if !hashing.New(costlier).NeedsRehash(bc) {
		t.Error("bcrypt cost changed")
	}
	if !hashing.New(cheap("bcrypt")).NeedsRehash(argon) {
		t.Error("argon2id hash under bcrypt driver")
	}
	if !low.NeedsRehash("garbage") {
		t.Error("malformed hash")
	}
}

func TestBcryptPasswordTooLong(t *testing.T) {
	_, err := hashing.New(cheap("bcrypt")).Make(strings.Repeat("a", 73))
	if !errors.Is(err, hashing.ErrPasswordTooLong) {
		t.Fatalf("err=%v", err)
	}
	if _, err := hashing.New(cheap("argon2id")).Make(strings.Repeat("a", 73)); err != nil {
		t.Fatal(err)
	}
}

func TestConfig(t *testing.T) {
	var cfg hashing.Config
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Driver != "argon2id" || cfg.Argon2id != (hashing.Argon2idConfig{Memory: 19456, Time: 2, Threads: 1}) || cfg.Bcrypt.Cost != 12 {
		t.Fatalf("defaults=%+v", cfg)
	}

	bad := hashing.Config{Driver: "md5", Argon2id: hashing.Argon2idConfig{Memory: 8, Time: 0, Threads: 2}, Bcrypt: hashing.BcryptConfig{Cost: 3}}
	err := bad.Validate()
	for _, want := range []string{"driver", "argon2id.time", "argon2id.memory", "bcrypt.cost"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s error: %v", want, err)
		}
	}

	defer func() {
		if recover() == nil {
			t.Error("New did not panic on invalid config")
		}
	}()
	hashing.New(bad)
}

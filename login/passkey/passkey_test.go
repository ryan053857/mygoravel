package passkey_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/hashing"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/login/passkey"
	"github.com/ryan053857/mygoravel/ratelimit"
)

const (
	rpID   = "blog.example.com"
	origin = "https://blog.example.com"
)

var (
	b64    = base64.RawURLEncoding
	appKey = crypt.GenerateKey()
)

// device 是软件实现的验证器：一个 ES256 密钥，保存在“设备”上的 passkey。
type device struct {
	key       *ecdsa.PrivateKey
	id        []byte // credential ID
	user      []byte // 用户句柄
	signCount uint32
	origin    string
}

func newDevice(t *testing.T) *device {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &device{key: key, id: id, origin: origin}
}

// options 是 Begin* 返回的选项中需要的部分。
type options struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
		ExcludeCredentials []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
	} `json:"publicKey"`
}

func parse(t *testing.T, c passkey.Ceremony) options {
	t.Helper()
	var o options
	if err := json.Unmarshal(c.Options, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func (d *device) clientData(t *testing.T, typ, challenge string) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": d.origin, "crossOrigin": false})
	return b
}

func (d *device) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte(rpID))
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, d.signCount)
	return append(out, attested...)
}

// create 模拟 navigator.credentials.create：返回客户端交给 FinishRegistration 的 JSON。
func (d *device) create(t *testing.T, c passkey.Ceremony) []byte {
	t.Helper()
	o := parse(t, c)
	d.user, _ = b64.DecodeString(o.PublicKey.User.ID)
	pub, err := d.key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	x, y := pub[1:33], pub[33:65]
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	attested := make([]byte, 16) // AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(d.id)))
	attested = append(append(attested, d.id...), cose...)
	// UP | UV | AT
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": d.authData(0x45, attested)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(d.id), "rawId": b64.EncodeToString(d.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(d.clientData(t, "webauthn.create", o.PublicKey.Challenge)),
			"attestationObject": b64.EncodeToString(att),
		},
	})
	return b
}

// get 模拟 navigator.credentials.get：返回客户端交给 FinishLogin 的 JSON。
func (d *device) get(t *testing.T, c passkey.Ceremony) []byte {
	t.Helper()
	o := parse(t, c)
	d.signCount++
	auth := d.authData(0x05, nil) // UP | UV
	client := d.clientData(t, "webauthn.get", o.PublicKey.Challenge)
	hash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(d.id), "rawId": b64.EncodeToString(d.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64.EncodeToString(client),
			"authenticatorData": b64.EncodeToString(auth),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(d.user),
		},
	})
	return b
}

const customer identity.SubjectType = "customer"

func ref(id string) identity.Ref { return identity.Ref{Type: customer, ID: id} }

type env struct {
	db       *bun.DB
	realm    *login.Realm
	passkeys *passkey.Store
}

// migrations 合并 login 和 passkey 的迁移；passkey 的版本号排在 login 之后。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"": login.Migrations(), "1": passkey.Migrations()} {
		err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, path)
			dir, name := path[:len(path)-len(d.Name())], d.Name()
			merged[dir+prefix+name] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return merged
}

func newEnv(t *testing.T, perTenant bool) *env {
	t.Helper()
	db := dbtest.Open(t, migrations(t))
	var hc hashing.Config
	hc.SetDefaults()
	hc.Driver, hc.Bcrypt.Cost = "bcrypt", 4
	c := crypt.New(crypt.Config{Key: appKey})
	loose := ratelimit.Config{Rate: 1000, Interval: time.Minute, Burst: 1000}
	realm := login.New(db, login.Options{
		Type: customer, PerTenant: perTenant, Hasher: hashing.New(hc), Crypt: c,
		Attempts: ratelimit.NewMemory(loose), Sends: ratelimit.NewMemory(loose),
	})
	store, err := passkey.New(db, realm, c, passkey.Config{RPID: rpID, RPName: "Blog", Origins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, realm: realm, passkeys: store}
}

// register 在 d 上为 ref 创建 passkey 并绑定；ref 为零值时注册新账号 newID。
func (e *env) register(t *testing.T, ctx context.Context, d *device, ref identity.Ref, newID string) {
	t.Helper()
	c, err := e.passkeys.BeginRegistration(ctx, ref, "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := e.passkeys.FinishRegistration(ctx, c.Token, d.create(t, c), "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if ref.IsZero() {
		if _, err := e.realm.Register(ctx, e.db, proof, identity.Ref{Type: customer, ID: newID}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := e.realm.Link(ctx, e.db, ref, proof); err != nil {
		t.Fatal(err)
	}
}

func (e *env) signIn(t *testing.T, ctx context.Context, d *device) (login.Outcome, error) {
	t.Helper()
	c, err := e.passkeys.BeginLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e.passkeys.FinishLogin(ctx, c.Token, d.get(t, c))
}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestPasskey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, false)

	// 用 passkey 注册新账号，然后用它登录。
	phone := newDevice(t)
	e.register(t, ctx, phone, identity.Ref{}, "c1")
	out, err := e.signIn(t, ctx, phone)
	if err != nil || out.Subject.Ref() != ref("c1") {
		t.Fatalf("sign in: %+v %v", out, err)
	}

	// 已登录的账号再添加一个 passkey：已有的 passkey 在选项中被排除，同一个账号用同一个用户句柄。
	laptop := newDevice(t)
	c, err := e.passkeys.BeginRegistration(ctx, ref("c1"), "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	o := parse(t, c)
	if len(o.PublicKey.ExcludeCredentials) != 1 || o.PublicKey.ExcludeCredentials[0].ID != b64.EncodeToString(phone.id) {
		t.Fatalf("exclusions=%+v", o.PublicKey.ExcludeCredentials)
	}
	again, _ := e.passkeys.BeginRegistration(ctx, ref("c1"), "alice@example.com")
	if parse(t, again).PublicKey.User.ID != o.PublicKey.User.ID {
		t.Fatal("user handle differs between registrations of the same account")
	}
	proof, err := e.passkeys.FinishRegistration(ctx, c.Token, laptop.create(t, c), "MacBook")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.realm.Link(ctx, e.db, ref("c1"), proof); err != nil {
		t.Fatal(err)
	}
	if out, err := e.signIn(t, ctx, laptop); err != nil || out.Subject.ID != "c1" {
		t.Fatalf("laptop: %+v %v", out, err)
	}

	list, err := e.passkeys.List(ctx, ref("c1"))
	if err != nil || len(list) != 2 || list[0].Name != "iPhone" || list[1].Name != "MacBook" || list[0].LastUsedAt.IsZero() {
		t.Fatalf("list=%+v %v", list, err)
	}
	if err := e.passkeys.Rename(ctx, ref("c1"), list[1].ID, " 公司电脑 "); err != nil {
		t.Fatal(err)
	}
	is(t, e.passkeys.Rename(ctx, ref("c2"), list[1].ID, "x"), passkey.ErrNotFound)

	// 移除：设备上的 passkey 不能再登录；最后一种登录方式不能移除。
	is(t, e.passkeys.Remove(ctx, e.db, ref("c2"), list[1].ID), passkey.ErrNotFound)
	if err := e.passkeys.Remove(ctx, e.db, ref("c1"), list[1].ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.signIn(t, ctx, laptop)
	is(t, err, passkey.ErrInvalid)
	is(t, e.passkeys.Remove(ctx, e.db, ref("c1"), list[0].ID), login.ErrLastIdentifier)
	if list, _ := e.passkeys.List(ctx, ref("c1")); len(list) != 1 {
		t.Fatalf("after remove: %+v", list)
	}
}

func TestPasskeyRejects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, false)
	d := newDevice(t)
	e.register(t, ctx, d, identity.Ref{}, "c1")

	// 签名计数倒退：疑似被克隆。
	if _, err := e.signIn(t, ctx, d); err != nil {
		t.Fatal(err)
	}
	d.signCount = 0
	_, err := e.signIn(t, ctx, d)
	is(t, err, passkey.ErrInvalid)
	d.signCount = 10

	// 其他网站的来源、错误的签名、注册仪式的 token 用来登录、伪造的 token。
	c, _ := e.passkeys.BeginLogin(ctx)
	phishing := *d
	phishing.origin = "https://blog-example.evil"
	_, err = e.passkeys.FinishLogin(ctx, c.Token, phishing.get(t, c))
	is(t, err, passkey.ErrInvalid)
	reg, _ := e.passkeys.BeginRegistration(ctx, identity.Ref{}, "x")
	_, err = e.passkeys.FinishLogin(ctx, reg.Token, d.get(t, c))
	is(t, err, passkey.ErrInvalid)
	_, err = e.passkeys.FinishLogin(ctx, "forged", d.get(t, c))
	is(t, err, passkey.ErrInvalid)
	_, err = e.passkeys.FinishLogin(ctx, c.Token, []byte("{}"))
	is(t, err, passkey.ErrInvalid)
	other := newDevice(t)
	other.user = d.user
	_, err = e.passkeys.FinishLogin(ctx, c.Token, other.get(t, c))
	is(t, err, passkey.ErrInvalid)

	// 注册后没有绑定的 passkey 不能登录，一天后被清理。
	stray := newDevice(t)
	rc, _ := e.passkeys.BeginRegistration(ctx, identity.Ref{}, "x")
	if _, err := e.passkeys.FinishRegistration(ctx, rc.Token, stray.create(t, rc), "stray"); err != nil {
		t.Fatal(err)
	}
	_, err = e.signIn(t, ctx, stray)
	is(t, err, passkey.ErrInvalid)
	if n, err := e.passkeys.Cleanup(ctx, 10); err != nil || n != 0 {
		t.Fatalf("cleanup of fresh passkeys: %d %v", n, err)
	}
	passkey.SetClock(e.passkeys, func() time.Time { return time.Now().Add(25 * time.Hour) })
	if n, err := e.passkeys.Cleanup(ctx, 10); err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	// 过期的仪式。
	_, err = e.passkeys.FinishLogin(ctx, c.Token, d.get(t, c))
	is(t, err, passkey.ErrInvalid)
}

// PerTenant：passkey 只能在注册时的租户中登录。
func TestPasskeyTenants(t *testing.T) {
	t.Parallel()
	acme := identity.WithTenant(context.Background(), "acme")
	globex := identity.WithTenant(context.Background(), "globex")
	e := newEnv(t, true)
	d := newDevice(t)
	e.register(t, acme, d, identity.Ref{}, "acme:1")
	if out, err := e.signIn(t, acme, d); err != nil || out.Subject.Tenant != "acme" {
		t.Fatalf("acme: %+v %v", out, err)
	}
	_, err := e.signIn(t, globex, d)
	is(t, err, passkey.ErrInvalid)
	// 在一个租户中发起的仪式不能在另一个租户中完成。
	c, _ := e.passkeys.BeginLogin(acme)
	_, err = e.passkeys.FinishLogin(globex, c.Token, d.get(t, c))
	is(t, err, passkey.ErrInvalid)
}

func TestNew(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, migrations(t))
	c := crypt.New(crypt.Config{Key: appKey})
	if _, err := passkey.New(db, nil, c, passkey.Config{RPID: rpID, Origins: []string{origin}}); err == nil {
		t.Fatal("nil realm accepted")
	}
}

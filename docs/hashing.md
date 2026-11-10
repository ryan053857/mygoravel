# 哈希

- [简介](#introduction)
- [配置](#configuration)
- [哈希与校验密码](#hashing-passwords)
- [重新哈希](#rehashing)

<a id="introduction"></a>
## 简介

密码不能明文保存，也不能用 SHA-256 这类快速哈希保存。`hashing` 包用专为密码设计的慢哈希算法生成哈希：默认是 argon2id，也可以选择 bcrypt。

哈希使用 PHC 字符串格式，例如 `$argon2id$v=19$m=19456,t=2,p=1$<盐>$<哈希>`。算法、参数和随机盐都编码在字符串中，保存在一个文本列里即可。这个格式与 PHP 的 `password_hash` 互通，从 Laravel 等应用迁移过来的密码哈希可以直接校验。

本章为博客应用的用户模块实现密码校验，也就是[页面登录](views.md#login)中登录处理器调用的 `user.Authenticate` action。

<a id="configuration"></a>
## 配置

把 `hashing.Config` 放进应用的根配置结构体，配置段为 `hashing`：

```go
type Config struct {
    // ...
    Hashing hashing.Config `yaml:"hashing"`
}
```

下面是全部配置项及其默认值，通常不需要写：

```yaml
hashing:
  driver: argon2id     # argon2id 或 bcrypt
  argon2id:
    memory: 19456      # 每次哈希使用的内存，单位 KiB
    time: 2            # 迭代次数
    threads: 1         # 并行度
  bcrypt:
    cost: 12           # 每加 1，耗时翻倍
```

argon2id 的默认值是 OWASP 推荐的最低配置。参数越大越难暴力破解，但每次登录也越慢、占用的内存越多。可以在生产机器上测一下，把单次哈希控制在几十到几百毫秒。

<a id="hashing-passwords"></a>
## 哈希与校验密码

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `Hasher`，保存在 `App` 中，传给需要它的 action：

```go
hasher := hashing.New(cfg.Hashing)
authenticate := user.NewAuthenticate(db, hasher)
```

`Hasher` 可以并发使用，整个应用共用一个即可。

保存密码时用 `Make` 生成哈希。每次调用都使用新的随机盐，同一个密码两次的结果不同：

```go
hash, err := a.hasher.Make(password)
if err != nil {
    return err
}
u := &User{Email: email, PasswordHash: hash}
```

登录时用 `Check` 校验密码。下面是用户模块的 `Authenticate` action，`ErrInvalidCredentials` 是应用定义的 `errorx.Unauthenticated` 类错误：

```go
var ErrInvalidCredentials = errorx.Define(errorx.Unauthenticated, "auth.invalid_credentials")

type Authenticate struct {
    db     *bun.DB
    hasher *hashing.Hasher
}

func NewAuthenticate(db *bun.DB, hasher *hashing.Hasher) *Authenticate {
    return &Authenticate{db: db, hasher: hasher}
}

func (a *Authenticate) Handle(ctx context.Context, email, password string) (*User, error) {
    var u User
    err := a.db.NewSelect().Model(&u).Where("email = ?", email).Scan(ctx)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrInvalidCredentials.New()
    }
    if err != nil {
        return nil, errors.Wrap(err, "find user")
    }
    if !a.hasher.Check(password, u.PasswordHash) {
        return nil, ErrInvalidCredentials.New()
    }
    return &u, nil
}
```

邮箱不存在和密码错误返回同一个错误，客户端无法据此判断某个邮箱是否注册过。防止暴力猜测密码，见[在处理器中限流](rate-limiting.md#in-handlers)。

bcrypt 只处理密码的前 72 字节。driver 为 bcrypt 时，`Make` 对更长的密码返回 `hashing.ErrPasswordTooLong`，请在注册接口的输入中用 `maxLength:"72"` 限制长度。argon2id 没有这个限制。

<a id="rehashing"></a>
## 重新哈希

`Check` 能校验 argon2id 和 bcrypt 两种哈希，与当前的 `driver` 无关。因此调高参数、切换算法或导入旧系统的 bcrypt 哈希之后，已有的密码仍然可以登录。

`NeedsRehash` 判断一个哈希是否用当前的算法和参数生成。登录时手上有明文密码，是更新哈希的唯一时机：

```go
if !a.hasher.Check(password, u.PasswordHash) {
    return nil, ErrInvalidCredentials.New()
}
if a.hasher.NeedsRehash(u.PasswordHash) {
    hash, err := a.hasher.Make(password)
    if err != nil {
        return nil, err
    }
    u.PasswordHash = hash
    if _, err := a.db.NewUpdate().Model(&u).Column("password_hash").WherePK().Exec(ctx); err != nil {
        return nil, errors.Wrap(err, "rehash password")
    }
}
return &u, nil
```

这样，用户在下次登录时就会自然地迁移到新的算法和参数。

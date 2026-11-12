# 哈希

- [简介](#introduction)
- [配置](#configuration)
- [哈希与校验密码](#hashing-passwords)
- [重新哈希](#rehashing)

<a id="introduction"></a>
## 简介

密码不能明文保存，也不能用 SHA-256 这类快速哈希保存。`hashing` 包用专为密码设计的慢哈希算法生成哈希：默认是 argon2id，也可以选择 bcrypt。

哈希使用 PHC 字符串格式，例如 `$argon2id$v=19$m=19456,t=2,p=1$<盐>$<哈希>`。算法、参数和随机盐都编码在字符串中，保存在一个文本列里即可。这个格式与 PHP 的 `password_hash` 互通，从 Laravel 等应用迁移过来的密码哈希可以直接校验。

以后还要读出明文的值（例如第三方 API Key）不能哈希，要用[加密](encryption.md#encrypting-data)。

账号的密码由[登录与注册](login.md)保存和校验，包括下面的重新哈希，不需要直接调用本章的方法。应用自己保存其他需要校验的口令时才直接使用 `Hasher`，本章以博客文章的访问口令为例：作者可以给文章设一个口令，读者输入正确才能阅读。

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

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `Hasher`，交给 [`login.New`](login.md#assembling) 和其他需要它的 action：

```go
hasher := hashing.New(cfg.Hashing)
```

`Hasher` 可以并发使用，整个应用共用一个即可。

保存口令时用 `Make` 生成哈希。每次调用都使用新的随机盐，同一个口令两次的结果不同：

```go
hash, err := a.hasher.Make(in.Passcode)
if err != nil {
    return err
}
_, err = a.db.NewUpdate().Model((*Article)(nil)).Set("passcode_hash = ?", hash).Where("id = ?", id).Exec(ctx)
```

校验时用 `Check`，它比较的是哈希，耗时与口令是否正确无关：

```go
if !a.hasher.Check(in.Passcode, art.PasscodeHash) {
    return nil, ErrWrongPasscode.New()
}
```

口令可以被反复猜测，校验接口要[限流](rate-limiting.md#in-handlers)。

bcrypt 只处理密码的前 72 字节。driver 为 bcrypt 时，`Make` 对更长的密码返回 `hashing.ErrPasswordTooLong`，请在输入中用 `maxLength:"72"` 限制长度。argon2id 没有这个限制。

<a id="rehashing"></a>
## 重新哈希

`Check` 能校验 argon2id 和 bcrypt 两种哈希，与当前的 `driver` 无关。因此调高参数、切换算法或导入旧系统的 bcrypt 哈希之后，已有的密码仍然可以校验。

`NeedsRehash` 判断一个哈希是否用当前的算法和参数生成。校验通过时手上有明文，是更新哈希的唯一时机：

```go
if a.hasher.NeedsRehash(art.PasscodeHash) {
    hash, err := a.hasher.Make(in.Passcode)
    if err != nil {
        return nil, err
    }
    // 保存新的哈希
}
```

`login` 在密码登录成功时就是这样做的，用户在下次登录时自然迁移到新的算法和参数。从其他系统导入账号时，把旧的 bcrypt 或 argon2id 哈希原样写入 `auth_passwords` 表即可。

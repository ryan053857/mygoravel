// Package hashing 对密码做单向哈希。
//
// New 按配置创建 Hasher：Make 用配置的算法（argon2id 或 bcrypt）生成哈希，
// Check 校验两种算法生成的哈希，NeedsRehash 判断哈希是否需要按当前配置重新生成。
// 哈希使用 PHC 字符串格式，与 PHP 的 password_hash 互通。
//
//	hasher := hashing.New(cfg.Hashing)
//	hash, err := hasher.Make(password)
//	ok := hasher.Check(password, hash)
package hashing

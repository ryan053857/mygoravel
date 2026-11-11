// Package crypt 用应用密钥加密数据和签名链接。
//
// New 按配置创建 Crypt。Encrypt 用 AES-256-GCM 加密以后还要读出明文的值，例如第三方 API Key；
// SignURL 给链接加上过期时间和签名，VerifyURL 校验收到的链接没有被改过、没有过期。
// 加密和签名使用从应用密钥派生的两把子密钥；轮换时旧密钥放进 PreviousKeys，仍然可以解密和验签。
//
//	c := crypt.New(cfg.Crypt)
//	stored := c.Encrypt(apiKey)
//	link := c.SignURL("https://blog.example.com/email/verify/42", 24*time.Hour)
//	err := c.VerifyURL(r.URL)
package crypt

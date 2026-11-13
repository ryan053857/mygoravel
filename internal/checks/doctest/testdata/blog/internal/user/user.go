// Package user 只声明文档中其他文件用到的部分：账号体系的名称见认证一章。
package user

import "github.com/go-tenon/tenon/identity"

// Type 是博客用户这套账号体系的名称，见认证一章的核心概念。
const Type identity.SubjectType = "user"

type User struct {
	ID          string
	Permissions []string
}

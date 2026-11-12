// Package editorviews 只声明完整示例一章用到的表单和组件，组件的写法见视图一章。
package editorviews

import (
	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/web"
)

type LoginInput struct {
	Email    string `json:"email" format:"email"`
	Password string `json:"password" minLength:"1"`
}

// CodeInput 是二次验证的表单，Pending 是隐藏字段。
type CodeInput struct {
	Pending string `json:"pending" minLength:"1"`
	Code    string `json:"code" minLength:"1"`
}

// PasswordInput 是设置密码的表单：Request、Code 来自邮件中的链接；验证过后改为带上 Proof。
type PasswordInput struct {
	Request  string `json:"request,omitempty"`
	Code     string `json:"code,omitempty"`
	Proof    string `json:"proof,omitempty"`
	Password string `json:"password" minLength:"1"`
}

type ConfirmInput struct {
	Password string `json:"password" minLength:"1"`
}

type InviteInput struct {
	Name  string `json:"name" minLength:"1"`
	Email string `json:"email" format:"email"`
}

func Login(*web.Form[LoginInput]) templ.Component          { return templ.NopComponent }
func TwoFactor(*web.Form[CodeInput]) templ.Component       { return templ.NopComponent }
func SetPassword(*web.Form[PasswordInput]) templ.Component { return templ.NopComponent }
func SetupTOTP(login.TOTPSetup) templ.Component            { return templ.NopComponent }
func RecoveryCodes([]string) templ.Component               { return templ.NopComponent }
func Confirm(*web.Form[ConfirmInput]) templ.Component      { return templ.NopComponent }
func Invite(*web.Form[InviteInput]) templ.Component        { return templ.NopComponent }

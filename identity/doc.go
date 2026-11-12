// Package identity 定义已认证的主体，并通过 context 传递身份。主体属于应用声明的某套账号体系（SubjectType），
// 框架保存主体的地方都按 Ref（类型和 ID）引用它，各账号体系的 ID 可以重复。
// 凭据校验由入口完成；Subject.Can 按主体的权限和凭据的限制判断能否执行某项操作，资源授权由业务代码（action）执行。
package identity

// Package identity 定义已认证的用户或服务主体，并通过 context 传递身份。
// 凭据校验由入口完成；Subject.Can 按主体的权限和凭据的限制判断能否执行某项操作，资源授权由业务代码（action）执行。
package identity

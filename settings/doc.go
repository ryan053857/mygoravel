// Package settings 保存运行时可修改的设置，例如站点名称、是否开放注册、租户的账单信息。
//
// 一组设置是一个结构体，整组保存为 settings 表中的一行 JSON，默认值写在代码中。
// 读取时把保存的 JSON 合并到默认值上，所以新增字段不需要迁移，删除的字段下次保存时去掉；
// 只有字段改名或改类型时，才需要写迁移转换保存的 JSON。
//
// 全局设置和每个租户的设置各占一行，互不影响。读取时不缓存，每次 Get 是一次主键查询。
//
//	site := settings.New(db, "site", Site{Name: "My Blog", SignupOpen: true})
//	s, err := site.Get(ctx)
//	err = site.Update(ctx, func(s *Site) error { s.SignupOpen = false; return nil })
//	b, err := billing.Tenant(sub.Tenant).Get(ctx)
package settings

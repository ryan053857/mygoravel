// Package authz 提供基于角色的权限：权限在代码中声明为 Catalog，角色是一组权限，
// 角色在某个域中分配给主体。域是租户 ID、平台本身（""）或所有租户（AllTenants）。
//
// 全局角色（Tenant 为 ""）是 owner、admin、member 这类模板，可以在任何租户和平台中分配；
// 租户也可以创建只在本租户中使用的角色。Store.Resolve 在每个请求中按 identity.TenantFromContext
// 加载主体的权限，可以直接作为 rest、web、realtime 的 Options.Accounts 中一套账号的 Resolver。
// 分配按主体的类型和 ID（identity.Ref）保存，同一个 Store 可以用于多套账号体系。
// 请求量大时用 Cache 选项把权限缓存在 Memcached 中，修改之后缓存立即失效。
//
//	store := authz.NewStore(db, catalog)
//	api := rest.New(rest.Options{Tenant: tenantOf, Accounts: identity.Accounts{Member: store.Resolve}})
//	role, err := store.CreateRole(ctx, authz.Role{Name: "editor", Permissions: []string{"articles:*"}})
//	err = store.Assign(ctx, identity.Ref{Type: Member, ID: userID}, tenantID, role.ID)
package authz

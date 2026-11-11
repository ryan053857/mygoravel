// Package feature 提供功能开关：在代码中声明开关和默认规则，运营人员在数据库中为全员、租户或用户修改设置。
//
// 判断顺序（第一条命中即返回）：
//
//  1. 全员设为 On 或 Off：直接返回，忽略租户和用户的单独设置。Off 是紧急关闭，On 是全量上线。
//  2. 用户的单独设置。
//  3. 租户的单独设置。
//  4. 全员设为 Percent(n)：按开关名与租户或用户 ID 的哈希放量，同一个人的结果稳定，提高比例时已开启的人仍然开启。
//  5. 都没有：调用 Flag.Default。
//
// 检查时不写数据库，也不缓存：每次 Active 是一次带索引的查询，For 一次读出一个主体相关的全部设置。
//
//	store := feature.NewStore(db, feature.Catalog{article.NewEditor})
//	if store.Active(ctx, sub, article.NewEditor) { ... }
//	err := store.Set(ctx, article.NewEditor, feature.Everyone, feature.Percent(20))
package feature

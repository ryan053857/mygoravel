// Package search 在应用自己的 PostgreSQL 中提供全文搜索。
//
// 索引用 Define 声明，文档类型的字段用 search 标签说明哪些参与全文检索、哪些用于过滤和排序：
//
//	var Articles = search.Define[ArticleDoc]("articles")
//
// 同步分两步。修改数据的事务中用 Sync 发布同步任务，事务提交后才同步，回滚时一起取消；
// worker 用 Handle 处理任务，LoadFunc 在同步事务中读取 key 当前的文档，写入索引，没有文档时从索引中删除：
//
//	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
//	    // ... 修改文章
//	    return Articles.Sync(ctx, tx, item.ID.String())
//	})
//
//	Articles.Handle(w, db, loadArticle, scanArticles)
//
// 修改了文档类型或者分词器之后，用 Reindex 重建：重建写入索引的新版本，期间同步同时写入新旧两个版本，
// 完成后切换，查询不会看到建了一半的索引。search/searchcmd 提供发起重建并显示进度的 search reindex 命令。
//
// Search 按查询文本、过滤条件、排序和分页查询，返回解码后的文档、命中词的摘要和分面统计：
//
//	res, err := Articles.Search(ctx, db, search.Query{Text: "全文搜索", Tenant: sub.Tenant, Page: in.OffsetParams})
//
// 分词在应用中完成，数据库只保存切分好的词，不需要 zhparser 之类的扩展。默认的 Bigram 不需要词典，
// 中文按相邻的两个字切分；需要按词语切分时使用 search/gse。
package search

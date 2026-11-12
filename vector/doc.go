// Package vector 在 Qdrant 中保存文档的向量，按语义检索。
//
// 数据以应用的数据库为准，Qdrant 中只是由它算出的副本。索引用 Define 声明，文档类型的字段用 vector 标签
// 说明哪段文本用来计算向量、哪些用于过滤：
//
//	var Chunks = vector.Define[ChunkDoc]("attachment-chunks")
//
// 同步与 search 包相同，分两步。修改数据的事务中用 Sync 发布同步任务，事务提交后才同步，回滚时一起取消；
// worker 用 Handle 处理任务，LoadFunc 在同步事务中读取 key 当前的文档，计算向量后写入 Qdrant，
// 没有文档时从索引中删除。文本没有变化的文档沿用原来的向量，不再调用模型：
//
//	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
//	    // ... 替换附件的分段
//	    return Chunks.Sync(ctx, tx, fileID.String())
//	})
//
//	Chunks.Handle(w, store, loadChunks, scanChunkKeys)
//
// Search 计算查询文本的向量，返回语义最接近的文档：
//
//	hits, err := Chunks.Search(ctx, store, vector.Query{Text: question, Tenant: sub.Tenant, Limit: 20})
//
// 换了模型或者计算向量的文本之后，用 Reindex 重建：重建用 Store 的模型写入索引的新版本（一个新的 collection），
// 期间同步同时写入新旧两个版本，完成后切换，查询不会看到建了一半的索引。每个版本记录了建立它的模型，
// 写入和查询总是用这个模型；换模型期间用 WithPrevious 传入旧模型。search/searchcmd 的 search reindex 命令
// 也能重建向量索引。
package vector

// Package page 定义列表接口的两种分页结构：固定分页与游标分页。
//
// 固定分页按页码跳转，响应带总数，适合后台表格等需要页码的场景：
//
//	type ListArticlesInput struct {
//		page.OffsetParams
//	}
//
//	var rows []Article
//	total, err := db.NewSelect().Model(&rows).
//		OrderExpr("created_at DESC, id DESC").
//		Limit(in.Limit()).Offset(in.Offset()).
//		ScanAndCount(ctx)
//	body := page.NewOffset(toDTOs(rows), in.OffsetParams, total)
//
// 游标分页从上一页的最后一条继续读取，不受并发写入影响，适合信息流和数据量大的列表。
// 游标是排序位置的不透明编码，结构由应用定义：
//
//	type articleKey struct {
//		At time.Time `json:"at"`
//		ID uuid.UUID `json:"id"`
//	}
//
//	q := db.NewSelect().Model(&rows).OrderExpr("published_at DESC, id DESC").Limit(in.Limit())
//	var after articleKey
//	if ok, err := in.After(&after); err != nil {
//		return nil, err
//	} else if ok {
//		q.Where("(published_at, id) < (?, ?)", after.At, after.ID)
//	}
//	// 扫描 rows 后：
//	body := page.NewCursor(toDTOs(rows), in.CursorParams, func(a ArticleDTO) any {
//		return articleKey{*a.PublishedAt, a.ID}
//	})
//
// 两种参数的 per_page 默认 20、最大 100，由 Huma 在调用处理器之前填充和校验。
package page

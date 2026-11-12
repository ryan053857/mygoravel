package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// 全文检索使用 search_entries.tokens：一个 tsvector，带 GIN 索引。
// 分词在 Go 中完成，直接写入 tsvector 的词，不经过 PostgreSQL 自己的分词规则（大小写、词干、停用词）。

// maxPositions 是 tsvector 中一个词最多记录的位置数。位置最大为 MaxTerms。
const maxPositions = 256

// lockKey 写入或更新 search_keys 中 key 的记录，锁住它直到事务结束。
func lockKey(ctx context.Context, tx bun.IDB, index, key string) error {
	_, err := tx.NewRaw(`INSERT INTO search_keys (index_name, doc_key, synced_at) VALUES (?, ?, ?) `+
		`ON CONFLICT (index_name, doc_key) DO UPDATE SET synced_at = excluded.synced_at`,
		index, key, time.Now().UTC()).Exec(ctx)
	return errors.Wrap(err, "search: lock key")
}

// replaceEntries 把 key 在 version 中原有的文档替换为 entries。
func replaceEntries(ctx context.Context, tx bun.IDB, index string, version int, key string, entries []entry) error {
	ids := tx.NewRaw(`SELECT id FROM search_entries WHERE index_name = ? AND version = ? AND doc_key = ?`, index, version, key)
	for _, q := range []*bun.RawQuery{
		tx.NewRaw(`DELETE FROM search_values WHERE entry_id IN (?)`, ids),
		tx.NewRaw(`DELETE FROM search_entries WHERE index_name = ? AND version = ? AND doc_key = ?`, index, version, key),
	} {
		if _, err := q.Exec(ctx); err != nil {
			return errors.Wrap(err, "search: delete entries")
		}
	}
	if len(entries) == 0 {
		return nil
	}

	entryIDs, err := insertEntries(ctx, tx, index, version, key, entries)
	if err != nil {
		return errors.Wrap(err, "search: insert entries")
	}
	var values []string
	var args []any
	for i, e := range entries {
		for name, vals := range e.values {
			for _, v := range vals {
				values = append(values, "(?, ?, ?)")
				args = append(args, entryIDs[i], name, v)
			}
		}
	}
	if len(values) > 0 {
		q := `INSERT INTO search_values (entry_id, field, value) VALUES ` + strings.Join(values, ", ")
		if _, err := tx.NewRaw(q, args...).Exec(ctx); err != nil {
			return errors.Wrap(err, "search: insert values")
		}
	}
	return nil
}

// unlockKey 删除 search_keys 中 key 的记录，key 已经没有文档时调用。
func unlockKey(ctx context.Context, tx bun.IDB, index, key string) error {
	_, err := tx.NewRaw(`DELETE FROM search_keys WHERE index_name = ? AND doc_key = ?`, index, key).Exec(ctx)
	return errors.Wrap(err, "search: delete key")
}

// insertEntries 写入 search_entries，按 entries 的顺序返回它们的 id。
func insertEntries(ctx context.Context, tx bun.IDB, index string, version int, key string, entries []entry) ([]int64, error) {
	rows, args := make([]string, len(entries)), []any{}
	for i, e := range entries {
		rows[i] = "(?, ?, ?, ?, ?, ?, ?::tsvector)"
		args = append(args, index, version, key, i, e.tenant, e.doc, tsvector(e.boost, e.body))
	}
	var inserted []struct {
		ID       int64 `bun:"id"`
		Position int   `bun:"position"`
	}
	err := tx.NewRaw(`INSERT INTO search_entries (index_name, version, doc_key, position, tenant, doc, tokens) VALUES `+
		strings.Join(rows, ", ")+` RETURNING id, position`, args...).Scan(ctx, &inserted)
	if err != nil {
		return nil, err
	}
	if len(inserted) != len(entries) {
		return nil, errors.Newf("search: inserted %d entries, got %d ids", len(entries), len(inserted))
	}
	ids := make([]int64, len(entries))
	for _, r := range inserted {
		ids[r.Position] = r.ID
	}
	return ids, nil
}

// tsvector 返回 tsvector 的文本形式：boost 中的词权重为 A，body 中的为 B，位置依次递增。
// ts_rank 默认给 A、B 两级 1.0 和 0.4 的权重，text,boost 字段的权重是普通文本字段的 2.5 倍。
func tsvector(boost, body []string) string {
	type lexeme struct {
		positions []string
	}
	lexemes := map[string]*lexeme{}
	var order []string
	pos := 0
	add := func(terms []string, weight string) {
		for _, t := range terms {
			pos++
			l := lexemes[t]
			if l == nil {
				l = &lexeme{}
				lexemes[t] = l
				order = append(order, t)
			}
			if len(l.positions) < maxPositions {
				l.positions = append(l.positions, fmt.Sprintf("%d%s", pos, weight))
			}
		}
	}
	add(boost, "A")
	add(body, "B")
	parts := make([]string, len(order))
	for i, t := range order {
		parts[i] = quoteLexeme(t) + ":" + strings.Join(lexemes[t].positions, ",")
	}
	return strings.Join(parts, " ")
}

// quoteLexeme 按 tsvector 和 tsquery 的语法给词加上引号。
func quoteLexeme(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `''`) + "'"
}

// tsquery 返回匹配全部 terms 的 tsquery 的文本形式，prefix 为 true 时最后一个词按前缀匹配。
func tsquery(q queryTerms) string {
	parts := make([]string, len(q.terms))
	for i, t := range q.terms {
		parts[i] = quoteLexeme(t)
	}
	if q.prefix {
		parts[len(parts)-1] += ":*"
	}
	return strings.Join(parts, " & ")
}

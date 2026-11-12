package search

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// boostWeight 是 text,boost 字段相对于普通文本字段的权重。PostgreSQL 的 ts_rank 默认给 A、B 两级
// 1.0 和 0.4 的权重，正好是这个比例。
const boostWeight = 2.5

// maxPositions 是 PostgreSQL tsvector 中一个词最多记录的位置数。位置最大为 MaxTerms。
const maxPositions = 256

// store 是一种数据库上的索引读写。三种数据库保存同样的数据，全文检索部分各用自己的索引：
//
//   - PostgreSQL：search_entries.tokens 是 tsvector，带 GIN 索引；
//   - SQLite：FTS5 虚拟表 search_fts，rowid 是 search_entries.id；
//   - MySQL：search_entries.boost 和 body 上的 FULLTEXT 索引。
//
// 分词在 Go 中完成，数据库只按空白切分：SQLite 和 MySQL 中的词编码为 t + 十六进制，
// 避免数据库自己的分词规则（大小写、最短词长、停用词）改变结果；PostgreSQL 直接写入 tsvector 的词。
type store struct{ dialect dialect.Name }

func storeOf(db bun.IDB) store { return store{dialect: db.Dialect().Name()} }

// txOptions 返回同步事务的选项。MySQL 用 READ COMMITTED，避免 REPEATABLE READ 的间隙锁让并发的同步死锁。
func (s store) txOptions() *sql.TxOptions {
	if s.dialect == dialect.MySQL {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return nil
}

// lockKey 写入或更新 search_keys 中 key 的记录，锁住它直到事务结束。
// MySQL 用 upsert 而不是 INSERT IGNORE 之后 FOR UPDATE：后者的共享锁在并发时会死锁。
func (s store) lockKey(ctx context.Context, tx bun.IDB, index, key string) error {
	q := `INSERT INTO search_keys (index_name, doc_key, synced_at) VALUES (?, ?, ?) ` +
		`ON CONFLICT (index_name, doc_key) DO UPDATE SET synced_at = excluded.synced_at`
	if s.dialect == dialect.MySQL {
		q = `INSERT INTO search_keys (index_name, doc_key, synced_at) VALUES (?, ?, ?) AS new ` +
			`ON DUPLICATE KEY UPDATE synced_at = new.synced_at`
	}
	_, err := tx.NewRaw(q, index, key, time.Now().UTC()).Exec(ctx)
	return errors.Wrap(err, "search: lock key")
}

// replace 删除 key 原有的文档，写入 entries；entries 为空时同时删除 search_keys 中的记录。
func (s store) replace(ctx context.Context, tx bun.IDB, index, key string, entries []entry) error {
	ids := tx.NewRaw(`SELECT id FROM search_entries WHERE index_name = ? AND doc_key = ?`, index, key)
	deletes := []*bun.RawQuery{tx.NewRaw(`DELETE FROM search_values WHERE entry_id IN (?)`, ids)}
	if s.dialect == dialect.SQLite {
		deletes = append(deletes, tx.NewRaw(`DELETE FROM search_fts WHERE rowid IN (?)`, ids))
	}
	deletes = append(deletes, tx.NewRaw(`DELETE FROM search_entries WHERE index_name = ? AND doc_key = ?`, index, key))
	if len(entries) == 0 {
		deletes = append(deletes, tx.NewRaw(`DELETE FROM search_keys WHERE index_name = ? AND doc_key = ?`, index, key))
	}
	for _, q := range deletes {
		if _, err := q.Exec(ctx); err != nil {
			return errors.Wrap(err, "search: delete entries")
		}
	}
	if len(entries) == 0 {
		return nil
	}

	entryIDs, err := s.insertEntries(ctx, tx, index, key, entries)
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

// insertEntries 写入 search_entries（SQLite 还有 search_fts），按 entries 的顺序返回它们的 id。
func (s store) insertEntries(ctx context.Context, tx bun.IDB, index, key string, entries []entry) ([]int64, error) {
	ids := make([]int64, len(entries))
	switch s.dialect {
	case dialect.MySQL:
		// MySQL 没有 RETURNING，逐行写入，用 LastInsertId 取得 id
		for i, e := range entries {
			res, err := tx.NewRaw(`INSERT INTO search_entries (index_name, doc_key, position, tenant, doc, boost, body) `+
				`VALUES (?, ?, ?, ?, ?, ?, ?)`, index, key, i, e.tenant, e.doc, encodeTerms(e.boost), encodeTerms(e.body)).Exec(ctx)
			if err != nil {
				return nil, err
			}
			if ids[i], err = res.LastInsertId(); err != nil {
				return nil, err
			}
		}
		return ids, nil
	case dialect.PG:
		rows, args := make([]string, len(entries)), []any{}
		for i, e := range entries {
			rows[i] = "(?, ?, ?, ?, ?, ?::tsvector)"
			args = append(args, index, key, i, e.tenant, e.doc, tsvector(e.boost, e.body))
		}
		err := s.returning(ctx, tx, `INSERT INTO search_entries (index_name, doc_key, position, tenant, doc, tokens) VALUES `+
			strings.Join(rows, ", ")+` RETURNING id, position`, args, ids)
		return ids, err
	default:
		rows, args := make([]string, len(entries)), []any{}
		for i, e := range entries {
			rows[i] = "(?, ?, ?, ?, ?)"
			args = append(args, index, key, i, e.tenant, e.doc)
		}
		err := s.returning(ctx, tx, `INSERT INTO search_entries (index_name, doc_key, position, tenant, doc) VALUES `+
			strings.Join(rows, ", ")+` RETURNING id, position`, args, ids)
		if err != nil {
			return nil, err
		}
		fts, args := make([]string, len(entries)), []any{}
		for i, e := range entries {
			fts[i] = "(?, ?, ?)"
			args = append(args, ids[i], encodeTerms(e.boost), encodeTerms(e.body))
		}
		_, err = tx.NewRaw(`INSERT INTO search_fts (rowid, boost, body) VALUES `+strings.Join(fts, ", "), args...).Exec(ctx)
		return ids, err
	}
}

// returning 执行带 RETURNING id, position 的写入，把 id 按 position 放入 ids。
func (s store) returning(ctx context.Context, tx bun.IDB, q string, args []any, ids []int64) error {
	var rows []struct {
		ID       int64 `bun:"id"`
		Position int   `bun:"position"`
	}
	if err := tx.NewRaw(q, args...).Scan(ctx, &rows); err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return errors.Newf("search: inserted %d entries, got %d ids", len(ids), len(rows))
	}
	for _, r := range rows {
		ids[r.Position] = r.ID
	}
	return nil
}

// encodeTerm 把词编码为 t + 十六进制，供 SQLite 和 MySQL 使用。前缀的编码也是完整编码的前缀，前缀匹配不受影响。
func encodeTerm(s string) string { return "t" + hex.EncodeToString([]byte(s)) }

func encodeTerms(terms []string) string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = encodeTerm(t)
	}
	return strings.Join(out, " ")
}

// tsvector 返回 PostgreSQL tsvector 的文本形式：boost 中的词权重为 A，body 中的为 B，位置依次递增。
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

// match 返回全文检索的条件和相关度表达式。
func (s store) match(q queryTerms) (cond string, condArgs []any, score string, scoreArgs []any) {
	switch s.dialect {
	case dialect.PG:
		parts := make([]string, len(q.terms))
		for i, t := range q.terms {
			parts[i] = quoteLexeme(t)
		}
		if q.prefix {
			parts[len(parts)-1] += ":*"
		}
		query := strings.Join(parts, " & ")
		return "e.tokens @@ ?::tsquery", []any{query}, "ts_rank(e.tokens, ?::tsquery)", []any{query}
	case dialect.MySQL:
		parts := make([]string, len(q.terms))
		for i, t := range q.terms {
			parts[i] = "+" + encodeTerm(t)
		}
		if q.prefix {
			parts[len(parts)-1] += "*"
		}
		words := encodeTerms(q.terms)
		return "MATCH (e.boost, e.body) AGAINST (? IN BOOLEAN MODE)", []any{strings.Join(parts, " ")},
			fmt.Sprintf("(MATCH (e.boost) AGAINST (?) * %g + MATCH (e.body) AGAINST (?))", boostWeight), []any{words, words}
	default:
		parts := make([]string, len(q.terms))
		for i, t := range q.terms {
			parts[i] = `"` + encodeTerm(t) + `"`
		}
		if q.prefix {
			parts[len(parts)-1] += "*"
		}
		return "search_fts MATCH ?", []any{strings.Join(parts, " ")},
			fmt.Sprintf("(-bm25(search_fts, %g, 1.0))", boostWeight), nil
	}
}

// from 返回查询的 FROM 子句：有全文检索条件时 SQLite 连接 search_fts。
func (s store) from(text bool) string {
	if text && s.dialect == dialect.SQLite {
		return "search_entries AS e JOIN search_fts ON search_fts.rowid = e.id"
	}
	return "search_entries AS e"
}

-- +goose Up
-- search_entries 是搜索索引中的文档，一个 key 可以有多个，按 position 排列。
-- tenant 为空字符串时是平台本身，或者索引没有租户；doc 是同步时保存的文档（JSON 文本）。
CREATE TABLE search_entries (
    id INTEGER PRIMARY KEY,
    index_name TEXT NOT NULL,
    doc_key TEXT NOT NULL,
    position INTEGER NOT NULL,
    tenant TEXT NOT NULL,
    doc TEXT NOT NULL,
    UNIQUE (index_name, doc_key, position)
);
CREATE INDEX search_entries_tenant ON search_entries (index_name, tenant);

-- search_fts 是全文索引，rowid 是 search_entries.id。词在应用中切分，编码为 t + 十六进制后写入，
-- 因此用 ascii 分词器按空白切分即可；表中不保存内容，只能按 rowid 删除。
CREATE VIRTUAL TABLE search_fts USING fts5 (boost, body, content = '', contentless_delete = 1, tokenize = 'ascii');

-- +goose Down
DROP TABLE search_fts;
DROP TABLE search_entries;

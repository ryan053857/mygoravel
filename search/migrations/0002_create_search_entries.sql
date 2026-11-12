-- +goose Up
CREATE TABLE search_entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    index_name text NOT NULL,
    version integer NOT NULL,
    doc_key text NOT NULL,
    position integer NOT NULL,
    tenant text NOT NULL,
    doc jsonb NOT NULL,
    tokens tsvector NOT NULL,
    UNIQUE (index_name, version, doc_key, position)
);
CREATE INDEX search_entries_tenant ON search_entries (index_name, version, tenant);
CREATE INDEX search_entries_tokens ON search_entries USING gin (tokens);
COMMENT ON TABLE search_entries IS '搜索索引中的文档，一个 key 可以有多个，按 position 排列';
COMMENT ON COLUMN search_entries.version IS '索引的版本，见 search_indexes';
COMMENT ON COLUMN search_entries.tenant IS '空字符串为平台本身，或者索引没有租户';
COMMENT ON COLUMN search_entries.doc IS '同步时保存的文档，搜索结果从这里解码';
COMMENT ON COLUMN search_entries.tokens IS '在应用中分词后写入的词，A 权重来自 text,boost 字段，B 来自其他文本字段';

-- +goose Down
DROP TABLE search_entries;

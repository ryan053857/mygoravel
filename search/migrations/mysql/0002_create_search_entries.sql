-- +goose Up
-- 搜索索引中的文档，一个 key 可以有多个，按 position 排列；version 是索引的版本，见 search_indexes。
-- tenant 为空字符串时是平台本身，或者索引没有租户；doc 是同步时保存的文档。
-- boost 和 body 是在应用中切分、编码为 t + 十六进制的词，boost 来自 text,boost 字段。
-- 编码之后每个词至少 3 个字符、只有字母和数字，不受 innodb_ft_min_token_size、停用词和排序规则的影响。
-- 三个 FULLTEXT 索引：(boost, body) 用于匹配，boost 和 body 分别用于计算带权重的相关度。
-- 索引写在 CREATE TABLE 中：MySQL 的 DDL 会隐式提交，一个迁移只执行一条 DDL。
CREATE TABLE search_entries (
    id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    index_name VARCHAR(64) NOT NULL,
    version INT NOT NULL,
    doc_key VARCHAR(191) NOT NULL,
    position INT NOT NULL,
    tenant VARCHAR(191) NOT NULL,
    doc JSON NOT NULL,
    boost MEDIUMTEXT CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    body MEDIUMTEXT CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    UNIQUE KEY search_entries_key (index_name, version, doc_key, position),
    INDEX search_entries_tenant (index_name, version, tenant),
    FULLTEXT INDEX search_entries_text (boost, body),
    FULLTEXT INDEX search_entries_boost (boost),
    FULLTEXT INDEX search_entries_body (body)
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE search_entries;

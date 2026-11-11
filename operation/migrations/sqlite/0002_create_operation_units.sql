-- +goose Up
-- 扇出到队列、还没有成功的单位，删除操作时级联删除。单位成功时删除，失败时保留，failed 为 1。id 是 UUID 文本。
CREATE TABLE operation_units (
    operation_id TEXT NOT NULL REFERENCES operations (id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    failed INTEGER NOT NULL,
    PRIMARY KEY (operation_id, id)
);

-- +goose Down
DROP TABLE operation_units;

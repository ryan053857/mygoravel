-- +goose Up
-- 扇出到队列、还没有成功的单位，删除操作时级联删除。单位成功时删除，失败时保留，failed 为 1。
CREATE TABLE operation_units (
    operation_id CHAR(36) NOT NULL,
    id CHAR(36) NOT NULL,
    failed BOOLEAN NOT NULL,
    PRIMARY KEY (operation_id, id),
    CONSTRAINT operation_units_operation FOREIGN KEY (operation_id) REFERENCES operations (id) ON DELETE CASCADE
) DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_bin;

-- +goose Down
DROP TABLE operation_units;

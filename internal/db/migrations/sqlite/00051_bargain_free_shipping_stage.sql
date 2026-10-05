-- +goose Up
CREATE TABLE bargain_free_shipping_stages (
    order_id TEXT NOT NULL,
    cookie_id TEXT NOT NULL,
    status TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(order_id, cookie_id)
);
CREATE INDEX idx_bargain_free_shipping_stages_status ON bargain_free_shipping_stages(status, order_id);

-- +goose Down
DROP INDEX idx_bargain_free_shipping_stages_status;
DROP TABLE bargain_free_shipping_stages;

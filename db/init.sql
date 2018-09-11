CREATE TABLE IF NOT EXISTS delivery_order (
  id       serial PRIMARY KEY,
  distance real   NOT NULL,
  is_taken bool   NOT NULL DEFAULT false
)

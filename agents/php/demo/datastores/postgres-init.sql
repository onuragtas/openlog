-- openlog PHP demo PostgreSQL schema (database `demo`, user demo/demo).
CREATE TABLE IF NOT EXISTS products (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  price NUMERIC(10, 2) NOT NULL,
  stock INT NOT NULL
);
INSERT INTO products (name, price, stock)
SELECT 'product-' || n, n * 2.25, (n * 7) % 50 FROM generate_series(1, 100) AS n;

CREATE TABLE IF NOT EXISTS reviews (
  id SERIAL PRIMARY KEY,
  product_id INT NOT NULL REFERENCES products (id),
  rating INT NOT NULL,
  body TEXT NOT NULL
);
INSERT INTO reviews (product_id, rating, body)
SELECT (n % 100) + 1, (n % 5) + 1, 'review ' || n FROM generate_series(1, 500) AS n;
CREATE INDEX IF NOT EXISTS idx_reviews_product ON reviews (product_id);

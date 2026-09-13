-- Spike schema: 100 rows read by both apps.
CREATE TABLE IF NOT EXISTS items (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  price DECIMAL(10, 2) NOT NULL
);
INSERT INTO items (name, price)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 100)
SELECT CONCAT('item-', n), n * 1.5 FROM seq;

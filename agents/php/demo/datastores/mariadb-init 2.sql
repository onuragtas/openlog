-- openlog PHP demo schema (database `demo`, user demo/demo) + the WordPress database.
CREATE DATABASE IF NOT EXISTS wordpress;
GRANT ALL PRIVILEGES ON wordpress.* TO 'demo'@'%';
FLUSH PRIVILEGES;

USE demo;

CREATE TABLE IF NOT EXISTS items (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  price DECIMAL(10, 2) NOT NULL
);
INSERT INTO items (name, price)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 100)
SELECT CONCAT('item-', n), n * 1.5 FROM seq;

CREATE TABLE IF NOT EXISTS users (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  email VARCHAR(128) NOT NULL
);
INSERT INTO users (name, email)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 20)
SELECT CONCAT('user-', n), CONCAT('user', n, '@demo.local') FROM seq;

CREATE TABLE IF NOT EXISTS orders (
  id INT PRIMARY KEY AUTO_INCREMENT,
  user_id INT NOT NULL,
  item_id INT NOT NULL,
  quantity INT NOT NULL,
  created_at DATETIME NOT NULL,
  KEY idx_orders_user (user_id)
);
INSERT INTO orders (user_id, item_id, quantity, created_at)
WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 400)
SELECT (n % 20) + 1, (n % 100) + 1, (n % 5) + 1, NOW() - INTERVAL n HOUR FROM seq;

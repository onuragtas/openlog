package io.github.onuragtas.openlog.testapp.mvc;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.util.List;
import java.util.Map;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import redis.clients.jedis.Jedis;

@RestController
public class DemoController {
  private static final Logger log = LoggerFactory.getLogger(DemoController.class);

  private final AppUserRepository users;
  private final JdbcTemplate jdbc;
  private final StringRedisTemplate redis;
  private final KafkaDemo kafka;
  private final GrpcDemo grpc;

  public DemoController(AppUserRepository users, JdbcTemplate jdbc, StringRedisTemplate redis, KafkaDemo kafka, GrpcDemo grpc) {
    this.users = users;
    this.jdbc = jdbc;
    this.redis = redis;
    this.kafka = kafka;
    this.grpc = grpc;
  }

  @GetMapping("/health")
  public String health() {
    return "ok";
  }

  @GetMapping("/hello")
  public Map<String, String> hello() {
    return Map.of("hello", "world");
  }

  @GetMapping("/users/{id}")
  public Map<String, Object> user(@PathVariable("id") long id) {
    log.info("loading user {}", id);
    String name = users.findById(id).map(AppUser::getName).orElse("");
    List<Map<String, Object>> rows =
        jdbc.queryForList("SELECT id, name FROM app_users WHERE name = 'alice' AND id IN (1, 2, 3)");
    return Map.of("id", id, "name", name, "rows", rows.size());
  }

  @GetMapping("/mysql/{id}")
  public Map<String, Object> mysql(@PathVariable("id") int id) throws Exception {
    String url =
        "jdbc:mysql://" + MvcApp.env("MYSQL_HOST", "127.0.0.1") + ":" + MvcApp.env("MYSQL_PORT", "23306")
            + "/openlog?useSSL=false&allowPublicKeyRetrieval=true";
    int count = 0;
    try (Connection c = DriverManager.getConnection(url, "openlog", "openlog");
        Statement st = c.createStatement()) {
      st.execute("CREATE TABLE IF NOT EXISTS items (id INT PRIMARY KEY, sku VARCHAR(64), qty INT)");
      st.execute("INSERT IGNORE INTO items VALUES (1, 'abc-1', 10)");
      try (ResultSet rs = st.executeQuery("SELECT id, sku FROM items WHERE sku = \"abc-1\" AND qty > 5")) {
        while (rs.next()) {
          count++;
        }
      }
    }
    return Map.of("id", id, "count", count);
  }

  @GetMapping("/redis/{key}")
  public Map<String, String> redis(@PathVariable("key") String key) {
    redis.opsForValue().set(key, "secret-value");
    String viaLettuce = redis.opsForValue().get(key);
    String viaJedis;
    try (Jedis j = new Jedis(MvcApp.env("REDIS_HOST", "127.0.0.1"), Integer.parseInt(MvcApp.env("REDIS_PORT", "23379")))) {
      j.set(key + ":jedis", "secret-value");
      viaJedis = j.get(key + ":jedis");
    }
    return Map.of("lettuce", String.valueOf(viaLettuce != null), "jedis", String.valueOf(viaJedis != null));
  }

  @PostMapping("/orders")
  public Map<String, String> order(@RequestParam("item") String item) throws Exception {
    kafka.send(item);
    log.info("order placed {}", item);
    return Map.of("item", item);
  }

  @GetMapping("/grpc/{name}")
  public Map<String, String> grpc(@PathVariable("name") String name) {
    return Map.of("reply", grpc.hello(name));
  }

  @GetMapping("/fail")
  public String fail() {
    throw new IllegalStateException("boom for order 42");
  }
}

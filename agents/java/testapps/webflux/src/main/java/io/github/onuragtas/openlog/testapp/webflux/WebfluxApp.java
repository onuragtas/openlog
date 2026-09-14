package io.github.onuragtas.openlog.testapp.webflux;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.event.EventListener;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Mono;

@SpringBootApplication
@RestController
public class WebfluxApp {
  private static final Logger log = LoggerFactory.getLogger(WebfluxApp.class);

  public static void main(String[] args) {
    SpringApplication.run(WebfluxApp.class, args);
  }

  @GetMapping("/health")
  public Mono<String> health() {
    return Mono.just("ok");
  }

  @GetMapping("/hello")
  public Mono<Map<String, String>> hello() {
    return Mono.just(Map.of("hello", "world"));
  }

  @GetMapping("/items/{id}")
  public Mono<Map<String, Object>> item(@PathVariable("id") String id) {
    log.info("loading item {}", id);
    return Mono.just(Map.of("id", id));
  }

  @GetMapping("/fail")
  public Mono<String> fail() {
    return Mono.error(new IllegalArgumentException("bad item 7"));
  }

  /** DEMO_LOAD_RPS (test/localagents demo): the app calls itself, every 20th request fails. Off by default. */
  @EventListener
  public void demoLoad(ApplicationReadyEvent event) {
    String rps = System.getenv("DEMO_LOAD_RPS");
    if (rps == null || rps.isBlank()) {
      return;
    }
    int port = event.getApplicationContext().getEnvironment().getProperty("local.server.port", Integer.class, 8080);
    long periodMs = Math.max(10, Math.round(1000 / Double.parseDouble(rps)));
    HttpClient client = HttpClient.newHttpClient();
    AtomicLong n = new AtomicLong();
    ScheduledExecutorService ex =
        Executors.newSingleThreadScheduledExecutor(
            r -> {
              Thread t = new Thread(r, "demo-load");
              t.setDaemon(true);
              return t;
            });
    ex.scheduleAtFixedRate(
        () -> {
          long i = n.incrementAndGet();
          String path = i % 20 == 0 ? "/fail" : "/items/" + (1 + i % 50);
          client.sendAsync(
              HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + port + path)).build(),
              HttpResponse.BodyHandlers.discarding());
        },
        1000,
        periodMs,
        TimeUnit.MILLISECONDS);
  }
}

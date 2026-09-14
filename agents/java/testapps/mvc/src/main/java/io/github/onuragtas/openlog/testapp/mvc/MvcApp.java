package io.github.onuragtas.openlog.testapp.mvc;

import org.springframework.boot.CommandLineRunner;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.context.annotation.Bean;

@SpringBootApplication
public class MvcApp {
  public static void main(String[] args) {
    SpringApplication.run(MvcApp.class, args);
  }

  static String env(String name, String def) {
    String v = System.getenv(name);
    return v == null || v.isBlank() ? def : v;
  }

  @Bean
  CommandLineRunner seedUsers(AppUserRepository users) {
    return args -> {
      if (users.count() == 0) {
        users.save(new AppUser(1L, "alice"));
        users.save(new AppUser(2L, "bob"));
      }
    };
  }
}

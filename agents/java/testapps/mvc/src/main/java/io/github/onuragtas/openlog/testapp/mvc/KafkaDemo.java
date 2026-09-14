package io.github.onuragtas.openlog.testapp.mvc;

import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import java.time.Duration;
import java.util.List;
import java.util.Properties;
import java.util.UUID;
import java.util.concurrent.TimeUnit;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.ConsumerRecords;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.apache.kafka.common.serialization.StringSerializer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Component;

/** Plain kafka-clients producer and a consumer thread (lazy: the producer is created on the first order). */
@Component
public class KafkaDemo {
  static final String TOPIC = "openlog-orders";
  private static final Logger log = LoggerFactory.getLogger(KafkaDemo.class);

  private final String bootstrap = MvcApp.env("KAFKA_BOOTSTRAP", "127.0.0.1:59092");
  private volatile boolean running = true;
  private KafkaProducer<String, String> producer;

  @PostConstruct
  void start() {
    if ("false".equals(System.getenv("KAFKA_ENABLED"))) {
      return;
    }
    Thread t = new Thread(this::consume, "orders-consumer");
    t.setDaemon(true);
    t.start();
  }

  private synchronized KafkaProducer<String, String> producer() {
    if (producer == null) {
      Properties p = new Properties();
      p.put("bootstrap.servers", bootstrap);
      p.put("key.serializer", StringSerializer.class.getName());
      p.put("value.serializer", StringSerializer.class.getName());
      p.put("max.block.ms", "30000");
      producer = new KafkaProducer<>(p);
    }
    return producer;
  }

  public void send(String item) throws Exception {
    producer().send(new ProducerRecord<>(TOPIC, item, item)).get(30, TimeUnit.SECONDS);
  }

  private void consume() {
    Properties p = new Properties();
    p.put("bootstrap.servers", bootstrap);
    p.put("group.id", "openlog-testapp-" + UUID.randomUUID());
    p.put("auto.offset.reset", "earliest");
    p.put("key.deserializer", StringDeserializer.class.getName());
    p.put("value.deserializer", StringDeserializer.class.getName());
    try (KafkaConsumer<String, String> consumer = new KafkaConsumer<>(p)) {
      consumer.subscribe(List.of(TOPIC));
      while (running) {
        ConsumerRecords<String, String> records = consumer.poll(Duration.ofMillis(200));
        for (ConsumerRecord<String, String> r : records) {
          log.info("consumed order {}", r.value());
        }
      }
    } catch (Exception e) {
      log.warn("kafka consumer stopped: {}", e.toString());
    }
  }

  @PreDestroy
  synchronized void stop() {
    running = false;
    if (producer != null) {
      producer.close(Duration.ofSeconds(2));
    }
  }
}

"""RabbitMQ (aio-pika) and Kafka (confluent-kafka, kafka-python, aiokafka) producers and consumers.

Run under openlog-instrument by tests/integration (RABBITMQ_URL, KAFKA_BOOTSTRAP). Every producer call runs inside a
"publish <client>" span and every consumer handler inside a "handle <client>" span.
"""

import asyncio
import os
import time
import uuid

from opentelemetry import trace

RUN = uuid.uuid4().hex[:8]
BOOTSTRAP = os.environ["KAFKA_BOOTSTRAP"]
TOPICS = {name: f"orders-{name}-{RUN}" for name in ("confluent", "kafka-python", "aiokafka")}
tracer = trace.get_tracer("messaging-app")


def create_topics():
    from kafka.admin import KafkaAdminClient, NewTopic

    admin = KafkaAdminClient(bootstrap_servers=BOOTSTRAP, request_timeout_ms=60000)
    try:
        admin.create_topics([NewTopic(t, num_partitions=1, replication_factor=1) for t in TOPICS.values()])
    finally:
        admin.close()


async def rabbitmq():
    import aio_pika

    connection = await aio_pika.connect_robust(os.environ["RABBITMQ_URL"])
    async with connection:
        channel = await connection.channel()
        queue = await channel.declare_queue(f"orders-{RUN}", auto_delete=True)
        received = asyncio.Event()

        async def on_message(message):
            async with message.process():
                with tracer.start_as_current_span("handle aio-pika"):
                    received.set()

        await queue.consume(on_message)
        with tracer.start_as_current_span("publish aio-pika"):
            await channel.default_exchange.publish(
                aio_pika.Message(b'{"order": 1}', message_id="m-1"), routing_key=queue.name
            )
        await asyncio.wait_for(received.wait(), 30)


def confluent():
    from confluent_kafka import Consumer, Producer

    topic = TOPICS["confluent"]
    producer = Producer({"bootstrap.servers": BOOTSTRAP})
    with tracer.start_as_current_span("publish confluent"):
        producer.produce(topic, b'{"order": 1}', key=b"order-1")
    assert producer.flush(30) == 0
    consumer = Consumer({"bootstrap.servers": BOOTSTRAP, "group.id": f"g-{RUN}", "auto.offset.reset": "earliest"})
    consumer.subscribe([topic])
    try:
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            msg = consumer.poll(1.0)
            if msg is not None and not msg.error():
                with tracer.start_as_current_span("handle confluent"):
                    pass
                break
        else:
            raise AssertionError("confluent-kafka: no message")
        consumer.poll(0.1)  # ends the consume span of the previous poll
    finally:
        consumer.close()


def kafka_python():
    from kafka import KafkaConsumer, KafkaProducer

    topic = TOPICS["kafka-python"]
    producer = KafkaProducer(bootstrap_servers=BOOTSTRAP)
    with tracer.start_as_current_span("publish kafka-python"):
        producer.send(topic, b'{"order": 1}', key=b"order-1").get(timeout=60)
    producer.close()
    consumer = KafkaConsumer(
        topic,
        bootstrap_servers=BOOTSTRAP,
        group_id=f"kp-{RUN}",
        auto_offset_reset="earliest",
        consumer_timeout_ms=60000,
    )
    try:
        for _record in consumer:
            with tracer.start_as_current_span("handle kafka-python"):
                pass
            break
        else:
            raise AssertionError("kafka-python: no message")
    finally:
        consumer.close()


async def aiokafka():
    from aiokafka import AIOKafkaConsumer, AIOKafkaProducer

    topic = TOPICS["aiokafka"]
    producer = AIOKafkaProducer(bootstrap_servers=BOOTSTRAP)
    await producer.start()
    try:
        with tracer.start_as_current_span("publish aiokafka"):
            await producer.send_and_wait(topic, b'{"order": 1}', key=b"order-1")
    finally:
        await producer.stop()
    consumer = AIOKafkaConsumer(topic, bootstrap_servers=BOOTSTRAP, group_id=f"ak-{RUN}", auto_offset_reset="earliest")
    await consumer.start()
    try:
        await asyncio.wait_for(consumer.getone(), 60)
        with tracer.start_as_current_span("handle aiokafka"):
            pass
    finally:
        await consumer.stop()


if __name__ == "__main__":
    create_topics()
    asyncio.run(rabbitmq())
    confluent()
    kafka_python()
    asyncio.run(aiokafka())
    print("messaging ok", RUN, flush=True)

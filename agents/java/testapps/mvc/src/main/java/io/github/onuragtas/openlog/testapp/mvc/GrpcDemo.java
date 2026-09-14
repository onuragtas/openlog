package io.github.onuragtas.openlog.testapp.mvc;

import io.grpc.CallOptions;
import io.grpc.Grpc;
import io.grpc.InsecureChannelCredentials;
import io.grpc.InsecureServerCredentials;
import io.grpc.ManagedChannel;
import io.grpc.MethodDescriptor;
import io.grpc.Server;
import io.grpc.ServerServiceDefinition;
import io.grpc.stub.ClientCalls;
import io.grpc.stub.ServerCalls;
import jakarta.annotation.PostConstruct;
import jakarta.annotation.PreDestroy;
import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.TimeUnit;
import org.springframework.stereotype.Component;

/** A gRPC server and client in the same process; plain string messages, so no protobuf code generation. */
@Component
public class GrpcDemo {
  static final MethodDescriptor.Marshaller<String> UTF8 =
      new MethodDescriptor.Marshaller<>() {
        @Override
        public InputStream stream(String value) {
          return new ByteArrayInputStream(value.getBytes(StandardCharsets.UTF_8));
        }

        @Override
        public String parse(InputStream stream) {
          try {
            return new String(stream.readAllBytes(), StandardCharsets.UTF_8);
          } catch (IOException e) {
            throw new UncheckedIOException(e);
          }
        }
      };

  static final MethodDescriptor<String, String> SAY_HELLO =
      MethodDescriptor.<String, String>newBuilder()
          .setType(MethodDescriptor.MethodType.UNARY)
          .setFullMethodName(MethodDescriptor.generateFullMethodName("demo.Greeter", "SayHello"))
          .setRequestMarshaller(UTF8)
          .setResponseMarshaller(UTF8)
          .build();

  private Server server;
  private ManagedChannel channel;

  @PostConstruct
  void start() throws IOException {
    ServerServiceDefinition service =
        ServerServiceDefinition.builder("demo.Greeter")
            .addMethod(
                SAY_HELLO,
                ServerCalls.asyncUnaryCall(
                    (request, observer) -> {
                      observer.onNext("hello " + request);
                      observer.onCompleted();
                    }))
            .build();
    server = Grpc.newServerBuilderForPort(0, InsecureServerCredentials.create()).addService(service).build().start();
    channel =
        Grpc.newChannelBuilderForAddress("127.0.0.1", server.getPort(), InsecureChannelCredentials.create()).build();
  }

  public String hello(String name) {
    return ClientCalls.blockingUnaryCall(channel, SAY_HELLO, CallOptions.DEFAULT.withDeadlineAfter(10, TimeUnit.SECONDS), name);
  }

  @PreDestroy
  void stop() {
    channel.shutdownNow();
    server.shutdownNow();
  }
}

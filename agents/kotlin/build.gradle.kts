// openlog mobile SDK for the JVM (docs/contracts/mobile-agent.md).
//
// Plain Kotlin/JVM, not an Android library: it needs HTTP, timers and JSON, none of which are Android APIs.
// An `com.android.library` project would put the Android SDK into CI to test code that does not use it, and
// would keep this out of JVM services and desktop clients for no gain. It is consumed from Android like any
// other JVM dependency.
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
  kotlin("jvm") version "2.1.0"
}

group = "io.github.onuragtas"
version = providers.gradleProperty("version").get()

dependencies {
  testImplementation(kotlin("test"))
}

// Java 11 bytecode rather than a toolchain: it builds on whatever JDK is present (no download), and it is
// what Android can actually load. A newer target would make the artifact unusable where it is aimed.
kotlin {
  compilerOptions { jvmTarget.set(JvmTarget.JVM_11) }
}

java {
  sourceCompatibility = JavaVersion.VERSION_11
  targetCompatibility = JavaVersion.VERSION_11
}

tasks.withType<JavaCompile>().configureEach { options.release.set(11) }

tasks.test { useJUnitPlatform() }

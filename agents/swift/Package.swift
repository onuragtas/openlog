// swift-tools-version: 5.9
// openlog mobile SDK for Apple platforms (docs/contracts/mobile-agent.md).
//
// No dependencies, on purpose: this is the one library an application cannot choose to drop, so it must
// never impose a version of another on it. Everything it needs — URLSession, JSONSerialization, timers — is
// in Foundation.

import PackageDescription

let package = Package(
    name: "OpenLog",
    // macOS is listed so `swift test` runs on a development machine and in CI without a simulator.
    platforms: [.iOS(.v13), .macOS(.v12), .tvOS(.v13), .watchOS(.v6)],
    products: [.library(name: "OpenLog", targets: ["OpenLog"])],
    targets: [
        .target(name: "OpenLog"),
        .testTarget(name: "OpenLogTests", dependencies: ["OpenLog"]),
    ]
)

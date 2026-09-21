# Mobile SDK for Apple platforms (OpenLog). Swift is not containerised the way the other agents are: the
# toolchain ships with Xcode and the package is tested where it is used.
.PHONY: build test clean

build:
	swift build

# `swift test` runs the package on macOS; the library targets iOS, tvOS and watchOS as well, which the
# build settles rather than the tests.
test:
	swift test

clean:
	rm -rf .build

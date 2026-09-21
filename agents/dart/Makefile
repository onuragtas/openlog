# Mobile SDK (package:openlog). Every target runs inside a dart container, so no local Dart is needed
# (DART=local runs the commands directly, e.g. in CI). Mirrors agents/browser/Makefile.
DART_VERSION ?= stable
PUB_VOLUME ?= openlog-dart-agent-pub

ifeq ($(DART),local)
RUN := sh -c
else
RUN := docker run --rm -v "$(CURDIR)":/src -v $(PUB_VOLUME):/root/.pub-cache -w /src dart:$(DART_VERSION) sh -c
endif

.PHONY: install analyze test clean

install:
	$(RUN) 'dart pub get'

# `dart analyze` is this package's typecheck and lint in one command.
analyze:
	$(RUN) 'dart pub get && dart analyze --fatal-infos'

test:
	$(RUN) 'dart pub get && dart test'

clean:
	rm -rf .dart_tool build

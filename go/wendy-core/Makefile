PYTHON ?= python3
BUILD_DIR := .build/sdk
PROTO := proto/wendy/core/v1/core.proto
VERSION := $(shell $(PYTHON) tools/build.py version)

.PHONY: all check generate native wasm javascript python kotlin swift documentation documentation-manifest compare-clean

all: check

$(BUILD_DIR)/wendy-core.pb: $(PROTO)
	@mkdir -p $(BUILD_DIR)
	protoc -I proto --include_imports --include_source_info --descriptor_set_out=$@ $(PROTO)

documentation-manifest: $(BUILD_DIR)/wendy-core.pb
	go run ./cmd/wendy-core-docs snapshot --headers include/WendyCore \
		--descriptor $(BUILD_DIR)/wendy-core.pb --version $(VERSION) \
		--output $(BUILD_DIR)/api.json --markdown $(BUILD_DIR)/protobuf-reference.md

generate: documentation-manifest
	$(PYTHON) tools/generate.py

native:
	$(PYTHON) tools/build.py native

wasm:
	$(PYTHON) tools/build.py wasm

javascript: native wasm
	cd distributions/javascript && npm ci
	$(PYTHON) tools/build.py node
	cd distributions/javascript && npx playwright install chromium && npm test && npm run test:types
	$(PYTHON) tools/build.py test-npm

python: native
	PYTHONPATH=distributions/python $(PYTHON) -m unittest discover -s distributions/python/tests -v

kotlin: native
	$(PYTHON) tools/build.py jni
	cd distributions/kotlin && ./gradlew test --no-daemon

swift: native
	@if [ "$$(uname -s)" = Darwin ]; then \
		xcodebuildmcp swift-package test --package-path "$(CURDIR)/distributions/swift"; \
	else cd distributions/swift && swift test; fi

check: generate native
	go test ./...
	go vet ./...
	$(PYTHON) -m unittest discover -s tools -p 'test_*.py' -v
	clang++ -x c++ -std=c++17 -fsyntax-only -Iinclude examples/c/hello.c
	printf '#include <WendyCore/WendyPlatform.h>\n' | clang -x c -std=c11 -fsyntax-only -Iinclude -
	printf '#include <WendyCore/WendyPlatform.h>\n' | clang++ -x c++ -std=c++17 -fsyntax-only -Iinclude -

compare-clean: documentation-manifest
	cmp documentation/versions/$(VERSION).json $(BUILD_DIR)/api.json

# Snapshot extraction does not depend on Doxygen; only HTML/XML rendering does.
documentation: documentation-manifest
	doxygen Doxyfile

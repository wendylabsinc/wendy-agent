GO_MODULE := github.com/wendylabsinc/townhall
BUILD_DIR := .build/sdk
DESCRIPTOR := $(BUILD_DIR)/wendy-sdk.pb
MANIFEST := $(BUILD_DIR)/api.json
PROTO := proto/wendy/sdk/v1/sdk.proto
HEADERS := include/Wendy

.PHONY: all generate check documentation documentation-manifest compare-clean

all: check

generate:
	@mkdir -p gen $(BUILD_DIR)/tools
	GOBIN="$(CURDIR)/$(BUILD_DIR)/tools" go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
	protoc \
		-I proto \
		--plugin=protoc-gen-go="$(CURDIR)/$(BUILD_DIR)/tools/protoc-gen-go" \
		--go_out=. \
		--go_opt=module=$(GO_MODULE) \
		$(PROTO)

$(DESCRIPTOR): $(PROTO)
	@mkdir -p $(BUILD_DIR)
	protoc -I proto --include_imports --include_source_info \
		--descriptor_set_out=$@ $(PROTO)

documentation-manifest: $(DESCRIPTOR)
	go run ./cmd/wendy-sdk-docs snapshot \
		--headers $(HEADERS) \
		--descriptor $(DESCRIPTOR) \
		--version 0.1.0-draft \
		--output $(MANIFEST) \
		--markdown $(BUILD_DIR)/ProtobufReference.md

documentation: documentation-manifest
	@command -v doxygen >/dev/null || { echo "doxygen is required to render HTML" >&2; exit 1; }
	doxygen Doxyfile

compare-clean: documentation-manifest
	go run ./cmd/wendy-sdk-docs compare \
		--from Documentation/Versions/0.1.0-draft.json \
		--to $(MANIFEST)

check: generate documentation-manifest
	git diff --exit-code -- gen
	go test ./...
	clang -std=c11 -fsyntax-only -Iinclude Examples/C/header-smoke.c
	clang++ -x c++ -std=c++17 -fsyntax-only -Iinclude Examples/C/header-smoke.c

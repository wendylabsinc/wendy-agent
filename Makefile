# Add platform detection at the top of the Makefile
OS := $(shell uname -s)
ARCH := $(shell uname -m)

# Determine platform-specific path
ifeq ($(OS),Darwin)
  PLATFORM := $(ARCH)-apple-macosx
else ifeq ($(OS),Linux)
  PLATFORM := $(ARCH)-unknown-linux-gnu
else
  PLATFORM := unknown
endif

.PHONY: all clean edge edge-agent help format setup-hooks build proto deps

help: ## Show this help message
	@echo 'Usage:'
	@echo '  make <product>'
	@echo ''
	@echo 'Targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

all: build-cli build-helper build-agent ## Build all executables

install-deps: ## Install dependencies needed for building
	brew install protoc-gen-grpc-swift

_protos:
	./scripts/generate-proto.sh

build-cli: _protos ## Build the edge CLI executable
	swift build --product edge
	echo "y" | cp -f .build/$(PLATFORM)/debug/edge ~/bin/edge-dev
	chmod +x ~/bin/edge-dev

build-helper: _protos ## Build the edge helper daemon executable
	swift build --product edge-helper
	echo "y" | cp -f .build/$(PLATFORM)/debug/edge-helper ~/bin/edge-helper
	chmod +x ~/bin/edge-helper

build-cli-linux: _protos ## build the edge CLI for linux with musl
	swiftly run swift build +6.1 --swift-sdk aarch64-swift-linux-musl --product edge -c release

build-agent: _protos ## Build the edge agent executable
	swiftly run swift build +6.1 --swift-sdk aarch64-swift-linux-musl --product edge-agent -c debug 
	cp .build/aarch64-swift-linux-musl/debug/edge-agent .
	chmod +x edge-agent
	@echo "Binary size: $$(du -h edge-agent | cut -f1)"

build-agent-release: _protos ## Build the edge agent executable in release mode
	swiftly run swift build +6.1 --swift-sdk aarch64-swift-linux-musl \
	--product edge-agent \
	-c release \
	-Xswiftc -whole-module-optimization \
	-Xlinker --gc-sections \
	-Xlinker --strip-all

	cp .build/aarch64-swift-linux-musl/release/edge-agent .
	strip edge-agent 2>/dev/null || true

	chmod +x edge-agent
	@echo "Binary size: $$(du -h edge-agent | cut -f1)"

test: ## Run the tests
	swift test

cov-html: ## Run the tests and generate coverage report
	swift test --enable-code-coverage
	xcrun llvm-cov show \
		.build/debug/edge-agentPackageTests.xctest/Contents/MacOS/edge-agentPackageTests \
		-instr-profile=.build/debug/codecov/default.profdata \
		Sources/ \
		--format=html \
		-o coverage_report
	open coverage_report/index.html

cov-report: ## Run the tests and generate coverage report
	swift test --enable-code-coverage
	xcrun llvm-cov export \
		.build/debug/edge-agentPackageTests.xctest/Contents/MacOS/edge-agentPackageTests \
		-instr-profile=.build/debug/codecov/default.profdata \
		--include-directory=Sources/ \
		-format=json > coverage.json
	xcrun llvm-cov export \
		.build/debug/edge-agentPackageTests.xctest/Contents/MacOS/edge-agentPackageTests \
		-instr-profile=.build/debug/codecov/default.profdata \
		--include-directory=Sources/ \
		-format=lcov > coverage.lcov
format: ## Format Swift code using swift-format
	swift format --recursive --in-place Sources/ Tests/

setup-hooks: ## Install git hooks
	./Scripts/install-hooks.sh

clean: ## Clean build artifacts and remove executables
	swift package clean
	rm -f edge EdgeAgent

# Default build target
build:
	@echo "Building edge-agent..."
	@mkdir -p bin
	@cd cmd/edge-agent && go build -o ../../bin/edge-agent

# Generate protocol buffer code
proto:
	@echo "Generating protobuf code..."
	@mkdir -p internal/proto
	@protoc \
		--go_out=. --go_opt=module=github.com/edgeengineer/edge-agent-go \
		--go-grpc_out=. --go-grpc_opt=module=github.com/edgeengineer/edge-agent-go \
		api/proto/edge/agent/services/v1/*.proto
	@# Move generated files to internal/proto
	@mkdir -p internal/proto/edge/agent/services/v1
	@mv api/proto/edge/agent/services/v1/*.pb.go internal/proto/edge/agent/services/v1/

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Default target
all: proto build 
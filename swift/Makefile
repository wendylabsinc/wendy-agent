.DEFAULT_GOAL := help

SWIFT_FORMAT_TARGETS := WendyAgentCore/Sources WendyAgentCore/Tests WendyAgentMac/Sources WendyE2ETests/Sources WendyE2ETests/Tests
SWIFT_LINT_TARGETS := WendyAgentCore/Sources WendyAgentCore/Tests WendyAgentMac/Sources

ifeq ($(OS),Windows_NT)
DETECTED_HOST_OS := windows
DETECTED_HOST_ENV := windows
else
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
DETECTED_HOST_OS := macos
DETECTED_HOST_ENV := unix
else ifeq ($(UNAME_S),Linux)
DETECTED_HOST_OS := linux
DETECTED_HOST_ENV := unix
else
DETECTED_HOST_OS := unknown
DETECTED_HOST_ENV := unix
endif
endif

HOST_OS ?= $(DETECTED_HOST_OS)
HOST_ENV ?= $(DETECTED_HOST_ENV)

ifeq ($(HOST_ENV),windows)
override SHELL := powershell.exe
.SHELLFLAGS := -NoProfile -ExecutionPolicy Bypass -Command
else
SHELL := /bin/bash
endif

REPO_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
E2E_OUTPUT_DIR := "$(REPO_ROOT)/Build/e2e"
WINDOWS_E2E_OUTPUT_DIR := $(E2E_OUTPUT_DIR)
UNIX_E2E_OUTPUT_DIR := $(E2E_OUTPUT_DIR)
RUN_E2E_TEST_WINDOWS = @if (-not $$PSVersionTable) { throw 'Windows E2E targets require PowerShell.' }; ./Scripts/E2ETest.ps1 --default-run-id "$(1)" $(2)
RUN_E2E_ANALYZE_WINDOWS = @if (-not $$PSVersionTable) { throw 'Windows E2E targets require PowerShell.' }; ./Scripts/E2EAnalyze.ps1 --output-dir $(WINDOWS_E2E_OUTPUT_DIR) --stage $(1)
REQUIRE_E2E_REVIEW_WINDOWS = @if (-not (Get-Command codex -ErrorAction SilentlyContinue) -and -not (Get-Command claude -ErrorAction SilentlyContinue)) { throw 'ERROR: e2e-review requires codex or claude.' }

OPEN_E2E_REPORT_UNIX = report_path="$(1)"; if [ -f "$$report_path" ]; then if [ "$$(uname -s)" = "Darwin" ]; then open "$$report_path" || echo "HTML report: $$report_path"; else echo "HTML report: $$report_path"; fi; else echo "HTML report not found: $$report_path"; fi
RUN_E2E_REPORT_WINDOWS = @if (-not $$PSVersionTable) { throw 'Windows report target requires PowerShell.' }; $$reportDir = '$(RUN_DIR)'; if (-not $$reportDir) { $$outputDir = $(WINDOWS_E2E_OUTPUT_DIR); if (Test-Path -LiteralPath $$outputDir) { $$reportDir = Get-ChildItem -LiteralPath $$outputDir -Directory -Filter 'swift-e2e-tests.*' | Where-Object { -not (Test-Path -LiteralPath (Join-Path $$_.FullName 'attempt.json') -PathType Leaf) } | Sort-Object LastWriteTime | Select-Object -Last 1 -ExpandProperty FullName } }; if (-not $$reportDir) { throw 'ERROR: RUN_DIR is required, or no run report directory was found.' }; ./Scripts/E2EReport.ps1 --run-dir $$reportDir; $$reportPath = Join-Path $$reportDir 'index.html'; if (Test-Path -LiteralPath $$reportPath) { Start-Process $$reportPath } else { Write-Output "HTML report not found: $$reportPath" }
RUN_E2E_REPORT_UNIX = @output_dir=$(UNIX_E2E_OUTPUT_DIR); \
	run_dir="$(RUN_DIR)"; \
	if [ -z "$$run_dir" ]; then \
		shopt -s nullglob; latest=""; latest_mtime=0; \
		for path in "$$output_dir"/swift-e2e-tests.*; do \
			[ -d "$$path" ] || continue; \
			[ ! -f "$$path/attempt.json" ] || continue; \
			mtime="$$(stat -f %m "$$path" 2>/dev/null || stat -c %Y "$$path" 2>/dev/null || echo 0)"; \
			if [ "$$mtime" -ge "$$latest_mtime" ]; then latest_mtime="$$mtime"; latest="$$path"; fi; \
		done; \
		shopt -u nullglob; run_dir="$$latest"; \
	fi; \
	if [ -z "$$run_dir" ]; then echo "ERROR: RUN_DIR is required, or no run report directory was found." >&2; exit 64; fi; \
	bash ./Scripts/E2EReport.sh --run-dir "$$run_dir"; \
	$(call OPEN_E2E_REPORT_UNIX,$$run_dir/index.html)
E2E_RUN_ID_HELPERS_UNIX = e2e_cli_id() { case "$$(uname -s)" in Darwin) major="$$(sw_vers -productVersion | cut -d. -f1)"; printf "macos-%s" "$$major" ;; Linux) if [ -r /etc/os-release ]; then . /etc/os-release; version="$${VERSION_ID//./-}"; printf "%s-%s" "$${ID:-linux}" "$$version"; else printf "linux"; fi ;; *) uname -s | tr '[:upper:]' '[:lower:]' | sed -E 's/[^[:alnum:]._-]+/-/g; s/-+/-/g; s/^-//; s/-$$//' ;; esac; }; next_e2e_run_id() { output_dir="$$1"; run_name="$$2"; base="swift-e2e-tests.local0000.$$run_name"; max=0; shopt -s nullglob; for path in "$$output_dir/$$base".[0-9][0-9][0-9][0-9]; do leaf="$$(basename "$$path")"; suffix="$$(printf '%s' "$$leaf" | awk -F. '{ print $$NF }')"; if [[ "$$suffix" =~ ^[0-9]{4}$$ ]]; then suffix_value="$$(expr "$$suffix" + 0)"; if [ "$$suffix_value" -gt "$$max" ]; then max="$$suffix_value"; fi; fi; done; shopt -u nullglob; printf "%s.%04d" "$$base" $$((max + 1)); }
RUN_E2E_TEST_UNIX = @output_dir=$(UNIX_E2E_OUTPUT_DIR); \
	$(E2E_RUN_ID_HELPERS_UNIX); \
	run_name="$$(e2e_cli_id)-to-$(1)"; \
	run_id="$${WENDY_E2E_RUN_ID:-$$(next_e2e_run_id "$$output_dir" "$$run_name")}"; \
	WENDY_E2E_RUN_ID="$$run_id" WENDY_E2E_RUN_NAME="$$run_name" bash ./Scripts/E2ETest.sh $(2)
REQUIRE_E2E_DEVICE_WINDOWS = @if (-not '$(strip $(DEVICE))') { throw 'ERROR: DEVICE is required. Usage: make $(1) DEVICE=<address>' }
REQUIRE_E2E_DEVICE_UNIX = @device='$(strip $(DEVICE))'; if [ -z "$$device" ]; then echo "ERROR: DEVICE is required. Usage: make $(1) DEVICE=<address>" >&2; exit 64; fi
RUN_E2E_ANALYZE_UNIX = @bash ./Scripts/E2EAnalyze.sh --output-dir $(UNIX_E2E_OUTPUT_DIR) --stage $(1)
REQUIRE_E2E_REVIEW_UNIX = @if ! command -v codex >/dev/null 2>&1 && ! command -v claude >/dev/null 2>&1; then echo "ERROR: e2e-review requires codex or claude." >&2; exit 64; fi

.PHONY: help
help: help-$(HOST_ENV) ## Show this help message

.PHONY: help-windows
help-windows:
	@if (-not $$PSVersionTable) { throw 'Windows make recipes require PowerShell.' }; Write-Output 'Usage:'; Write-Output '  make <target>'; Write-Output ''; Write-Output 'Targets:'; Select-String -Path '$(firstword $(MAKEFILE_LIST))' -Pattern '^[a-zA-Z0-9_-]+:.*?\#\# ' | Where-Object { -not ($$_.Line -match '^e2e-') } | ForEach-Object { $$parts = $$_.Line -split ':.*?\#\# ', 2; '  {0,-40} {1}' -f $$parts[0], $$parts[1] }; Write-Output ''; Write-Output 'Swift E2E targets:'; '  {0,-40} {1}' -f 'e2e-test', 'Run Swift E2E tests locally and write attempt artifacts'; '  {0,-40} {1}' -f 'e2e-test-linux', 'Run Swift E2E tests against a Linux device; requires DEVICE'; '  {0,-40} {1}' -f 'e2e-test-macos', 'Run Swift E2E tests against a macOS device; requires DEVICE'; '  {0,-40} {1}' -f 'e2e-test-wendy', 'Run Swift E2E tests against a WendyOS device; requires DEVICE'; '  {0,-40} {1}' -f 'e2e-aggregate', 'Aggregate all Swift E2E attempts'; '  {0,-40} {1}' -f 'e2e-review', 'Review Swift E2E run results with AI'; '  {0,-40} {1}' -f 'e2e-report', 'Render Swift E2E run report and open the latest'; '  {0,-40} {1}' -f 'e2e-reference', 'Generate Swift E2E HTML reference documentation and open it'; '  {0,-40} {1}' -f 'e2e-analyze', 'Run e2e-aggregate, e2e-review, and e2e-report'; Write-Output ''; Write-Output 'Device shortcut targets:'; '  {0,-40} {1}' -f 'e2e-test-wendy-<name>', 'Run WendyOS E2E tests against <name>.local'; '  {0,-40} {1}' -f 'e2e-test-linux-<name>', 'Run Linux E2E tests against <name>.local'; '  {0,-40} {1}' -f 'e2e-test-macos-<name>', 'Run macOS E2E tests against <name>.local'; '  {0,-40} {1}' -f 'e2e-test-mac-mini', 'Run Swift E2E tests against mac-mini.local'; '  {0,-40} {1}' -f 'e2e-test-jetson-orin-nano', 'Run Swift E2E tests against wendyos-jetson-orin-nano.local'; '  {0,-40} {1}' -f 'e2e-test-raspberry-pi-5', 'Run Swift E2E tests against wendyos-raspberry-pi-5.local'

.PHONY: help-unix
help-unix:
	@echo 'Usage:'; echo '  make <target>'; echo ''; echo 'Targets:'; grep -E '^[a-zA-Z0-9_-]+:.*?\#\# .*$$' $(MAKEFILE_LIST) | grep -v -E '^e2e-' | sort | awk 'BEGIN {FS = ":.*?\#\# "}; {printf "  %-40s %s\n", $$1, $$2}'; echo ''; echo 'Swift E2E targets:'; printf "  %-40s %s\n" 'e2e-test' 'Run Swift E2E tests locally and write attempt artifacts' 'e2e-test-linux' 'Run Swift E2E tests against a Linux device; requires DEVICE' 'e2e-test-macos' 'Run Swift E2E tests against a macOS device; requires DEVICE' 'e2e-test-wendy' 'Run Swift E2E tests against a WendyOS device; requires DEVICE' 'e2e-aggregate' 'Aggregate all Swift E2E attempts' 'e2e-review' 'Review Swift E2E run results with AI' 'e2e-report' 'Render Swift E2E run report and open the latest' 'e2e-reference' 'Generate Swift E2E HTML reference documentation and open it' 'e2e-analyze' 'Run e2e-aggregate, e2e-review, and e2e-report'; echo ''; echo 'Device shortcut targets:'; printf "  %-40s %s\n" 'e2e-test-wendy-<name>' 'Run WendyOS E2E tests against <name>.local' 'e2e-test-linux-<name>' 'Run Linux E2E tests against <name>.local' 'e2e-test-macos-<name>' 'Run macOS E2E tests against <name>.local' 'e2e-test-mac-mini' 'Run Swift E2E tests against mac-mini.local' 'e2e-test-jetson-orin-nano' 'Run Swift E2E tests against wendyos-jetson-orin-nano.local' 'e2e-test-raspberry-pi-5' 'Run Swift E2E tests against wendyos-raspberry-pi-5.local'

.PHONY: FORCE
FORCE:

.PHONY: format
format: format-$(HOST_ENV) ## Format Swift code using swift-format

.PHONY: format-windows
format-windows:
	swift format --recursive --in-place $(SWIFT_FORMAT_TARGETS)

.PHONY: format-unix
format-unix:
	swift format --recursive --in-place $(SWIFT_FORMAT_TARGETS)

.PHONY: lint
lint: lint-$(HOST_ENV) ## Lint Swift code using swift-format

.PHONY: lint-windows
lint-windows:
	swift format lint --recursive --strict $(SWIFT_LINT_TARGETS)

.PHONY: lint-unix
lint-unix:
	bash ./Scripts/Lint.sh

.PHONY: test
test: test-$(HOST_ENV) ## Run Swift package tests

.PHONY: test-windows
test-windows:
	@if (-not $$PSVersionTable) { throw 'Windows test target requires PowerShell.' }; New-Item -ItemType Directory -Force -Path Build/SwiftPM | Out-Null; Push-Location WendyAgentCore; try { swift test --scratch-path ../Build/SwiftPM; if ($$LASTEXITCODE -ne 0) { exit $$LASTEXITCODE } } finally { Pop-Location }

.PHONY: test-unix
test-unix:
	bash ./Scripts/Test.sh

e2e-test: DEVICE ?= wendyos-raspberry-pi-5.local
.PHONY: e2e-test
e2e-test: e2e-test-$(HOST_ENV) ## Run Swift E2E tests locally and write attempt artifacts

.PHONY: e2e-test-windows
e2e-test-windows:
	$(call RUN_E2E_TEST_WINDOWS,windows-to-raspberry-pi-5,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os wendyOS --agent-address "$(DEVICE)")

.PHONY: e2e-test-unix
e2e-test-unix:
	$(call RUN_E2E_TEST_UNIX,local,--output-dir $(UNIX_E2E_OUTPUT_DIR) --cli-root-dir $(UNIX_E2E_OUTPUT_DIR) --agent-root-dir $(UNIX_E2E_OUTPUT_DIR) --parallel)

.PHONY: e2e-analyze
e2e-analyze: e2e-aggregate e2e-review e2e-report ## Run e2e-aggregate, e2e-review, and e2e-report

.PHONY: e2e-aggregate
e2e-aggregate: e2e-aggregate-$(HOST_ENV) ## Aggregate all Swift E2E attempts

.PHONY: e2e-aggregate-windows
e2e-aggregate-windows:
	$(call RUN_E2E_ANALYZE_WINDOWS,aggregate)

.PHONY: e2e-aggregate-unix
e2e-aggregate-unix:
	$(call RUN_E2E_ANALYZE_UNIX,aggregate)

.PHONY: e2e-review
e2e-review: e2e-review-$(HOST_ENV) ## Review Swift E2E run results with AI

.PHONY: e2e-review-windows
e2e-review-windows:
	$(REQUIRE_E2E_REVIEW_WINDOWS)
	$(call RUN_E2E_ANALYZE_WINDOWS,review)

.PHONY: e2e-review-unix
e2e-review-unix:
	$(REQUIRE_E2E_REVIEW_UNIX)
	$(call RUN_E2E_ANALYZE_UNIX,review)

.PHONY: e2e-report
e2e-report: e2e-report-$(HOST_ENV) ## Render Swift E2E run report and open the latest

.PHONY: e2e-report-windows
e2e-report-windows:
	$(call RUN_E2E_ANALYZE_WINDOWS,report)

.PHONY: e2e-report-unix
e2e-report-unix:
	$(call RUN_E2E_ANALYZE_UNIX,report)

.PHONY: e2e-test-wendy
e2e-test-wendy: e2e-test-wendy-$(HOST_ENV) ## Run Swift E2E tests against a WendyOS device; requires DEVICE

.PHONY: e2e-test-wendy-windows
e2e-test-wendy-windows:
	$(call REQUIRE_E2E_DEVICE_WINDOWS,e2e-test-wendy)
	$(call RUN_E2E_TEST_WINDOWS,wendy,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os wendy --agent-address "$(DEVICE)")

.PHONY: e2e-test-wendy-unix
e2e-test-wendy-unix:
	$(call REQUIRE_E2E_DEVICE_UNIX,e2e-test-wendy)
	$(call RUN_E2E_TEST_UNIX,wendy,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os wendy --agent-address "$(DEVICE)")

.PHONY: e2e-test-linux
e2e-test-linux: e2e-test-linux-$(HOST_ENV) ## Run Swift E2E tests against a Linux device; requires DEVICE

.PHONY: e2e-test-linux-windows
e2e-test-linux-windows:
	$(call REQUIRE_E2E_DEVICE_WINDOWS,e2e-test-linux)
	$(call RUN_E2E_TEST_WINDOWS,linux,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os linux --agent-address "$(DEVICE)")

.PHONY: e2e-test-linux-unix
e2e-test-linux-unix:
	$(call REQUIRE_E2E_DEVICE_UNIX,e2e-test-linux)
	$(call RUN_E2E_TEST_UNIX,linux,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os linux --agent-address "$(DEVICE)")

.PHONY: e2e-test-macos
e2e-test-macos: e2e-test-macos-$(HOST_ENV) ## Run Swift E2E tests against a macOS device; requires DEVICE

.PHONY: e2e-test-macos-windows
e2e-test-macos-windows:
	$(call REQUIRE_E2E_DEVICE_WINDOWS,e2e-test-macos)
	$(call RUN_E2E_TEST_WINDOWS,macos,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os macos --agent-address "$(DEVICE)")

.PHONY: e2e-test-macos-unix
e2e-test-macos-unix:
	$(call REQUIRE_E2E_DEVICE_UNIX,e2e-test-macos)
	$(call RUN_E2E_TEST_UNIX,macos,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os macos --agent-address "$(DEVICE)")

e2e-test-wendy-%: FORCE
	$(MAKE) e2e-test-wendy DEVICE="$*.local"

e2e-test-linux-%: FORCE
	$(MAKE) e2e-test-linux DEVICE="$*.local"

e2e-test-macos-%: FORCE
	$(MAKE) e2e-test-macos DEVICE="$*.local"

e2e-test-jetson-orin-nano: DEVICE ?= wendyos-jetson-orin-nano.local
.PHONY: e2e-test-jetson-orin-nano
e2e-test-jetson-orin-nano: e2e-test-jetson-orin-nano-$(HOST_ENV)

.PHONY: e2e-test-jetson-orin-nano-windows
e2e-test-jetson-orin-nano-windows:
	$(call RUN_E2E_TEST_WINDOWS,jetson-orin-nano,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os wendyOS --agent-address "$(DEVICE)")

.PHONY: e2e-test-jetson-orin-nano-unix
e2e-test-jetson-orin-nano-unix:
	$(call RUN_E2E_TEST_UNIX,jetson-orin-nano,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os wendyOS --agent-address "$(DEVICE)")

e2e-test-raspberry-pi-5: DEVICE ?= wendyos-raspberry-pi-5.local
.PHONY: e2e-test-raspberry-pi-5
e2e-test-raspberry-pi-5: e2e-test-raspberry-pi-5-$(HOST_ENV)

.PHONY: e2e-test-raspberry-pi-5-windows
e2e-test-raspberry-pi-5-windows:
	$(call RUN_E2E_TEST_WINDOWS,raspberry-pi-5,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os wendyOS --agent-address "$(DEVICE)")

.PHONY: e2e-test-raspberry-pi-5-unix
e2e-test-raspberry-pi-5-unix:
	$(call RUN_E2E_TEST_UNIX,raspberry-pi-5,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os wendyOS --agent-address "$(DEVICE)")

e2e-test-mac-mini: DEVICE ?= mac-mini.local
.PHONY: e2e-test-mac-mini
e2e-test-mac-mini: e2e-test-mac-mini-$(HOST_ENV)

.PHONY: e2e-test-mac-mini-windows
e2e-test-mac-mini-windows:
	$(call RUN_E2E_TEST_WINDOWS,mac-mini,--output-dir $(WINDOWS_E2E_OUTPUT_DIR) --agent-os macOS --agent-address "$(DEVICE)")

.PHONY: e2e-test-mac-mini-unix
e2e-test-mac-mini-unix:
	$(call RUN_E2E_TEST_UNIX,mac-mini,--output-dir $(UNIX_E2E_OUTPUT_DIR) --agent-os macOS --agent-address "$(DEVICE)")

.PHONY: report
report: report-$(HOST_ENV) ## Render the latest Swift E2E run report and open it

.PHONY: report-windows
report-windows:
	$(RUN_E2E_REPORT_WINDOWS)

.PHONY: report-unix
report-unix:
	$(RUN_E2E_REPORT_UNIX)

.PHONY: e2e-reference
e2e-reference: reference ## Generate Swift E2E HTML reference documentation and open it

.PHONY: reference
reference: reference-$(HOST_ENV) ## Generate Swift E2E HTML reference documentation and open it

.PHONY: reference-windows
reference-windows:
	@if (-not $$PSVersionTable) { throw 'Windows reference target requires PowerShell.' }; Remove-Item -LiteralPath Build/Reference -Recurse -Force -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force -Path Build/Reference | Out-Null; Push-Location WendyE2ETests; try { swift run swift-e2e-testing reference --format html --output ../Build/Reference Tests/WendyE2ETests; if ($$LASTEXITCODE -ne 0) { exit $$LASTEXITCODE } } finally { Pop-Location }; Start-Process Build/Reference/index.html

.PHONY: reference-unix
reference-unix:
	rm -rf Build/Reference
	mkdir -p Build/Reference
	cd WendyE2ETests && swift run swift-e2e-testing reference --format html --output ../Build/Reference Tests/WendyE2ETests
	open Build/Reference/index.html

.PHONY: proto
proto: proto-$(HOST_ENV) ## Generate Swift gRPC code from Proto/

.PHONY: proto-windows
proto-windows:
	@throw 'proto is not supported on Windows yet; use a unix host for Scripts/GenerateProto.sh.'

.PHONY: proto-unix
proto-unix:
	./Scripts/GenerateProto.sh

.PHONY: clean
clean: clean-$(HOST_ENV) ## Remove Build/

.PHONY: clean-windows
clean-windows:
	Remove-Item -LiteralPath Build -Recurse -Force -ErrorAction SilentlyContinue

.PHONY: clean-unix
clean-unix:
	rm -rf Build/

.PHONY: agent-release
agent-release: agent-release-$(HOST_OS) ## Build, sign, notarize, and package the macOS app

.PHONY: agent-release-macos
agent-release-macos:
	VERSION='$(VERSION)' OUTPUT_DIR='$(OUTPUT_DIR)' bash ./Scripts/Build.sh

.PHONY: agent-release-linux
agent-release-linux:
	@echo 'ERROR: agent-release is macOS-only.' >&2; exit 1

.PHONY: agent-release-windows
agent-release-windows:
	@throw 'agent-release is macOS-only.'

.PHONY: build-cli
build-cli: build-cli-$(HOST_ENV) ## Build the Go CLI in ../go

.PHONY: build-cli-windows
build-cli-windows:
	$(MAKE) -C ../go build-cli HOST_ENV=windows

.PHONY: build-cli-unix
build-cli-unix:
	$(MAKE) -C ../go build-cli HOST_ENV=unix

.PHONY: discover
discover: build-cli discover-$(HOST_ENV) ## Build the Go CLI and discover Wendy devices

.PHONY: discover-windows
discover-windows:
	../go/bin/wendy.exe discover

.PHONY: discover-unix
discover-unix:
	../go/bin/wendy discover

.PHONY: agent-build
agent-build: agent-build-$(HOST_OS) ## Build and package a dev-signed macOS app without notarization

.PHONY: agent-build-macos
agent-build-macos:
	OUTPUT_DIR='$(OUTPUT_DIR)' bash ./Scripts/Build.sh --dev

.PHONY: agent-build-linux
agent-build-linux:
	@echo 'ERROR: agent-build is macOS-only.' >&2; exit 1

.PHONY: agent-build-windows
agent-build-windows:
	@throw 'agent-build is macOS-only.'

.PHONY: agent-start
agent-start: agent-start-$(HOST_OS) ## Stop, build, and open the WendyAgentMac app

.PHONY: agent-start-macos
agent-start-macos:
	@$(MAKE) agent-stop
	@$(MAKE) agent-build
	open Build/WendyAgentMac.app

.PHONY: agent-start-linux
agent-start-linux:
	@echo 'ERROR: agent-start is macOS-only.' >&2; exit 1

.PHONY: agent-start-windows
agent-start-windows:
	@throw 'agent-start is macOS-only.'

.PHONY: agent-reset-settings
agent-reset-settings: agent-reset-settings-$(HOST_OS) ## Reset WendyAgentMac preferences/defaults

.PHONY: agent-reset-settings-macos
agent-reset-settings-macos:
	./Scripts/ResetMacAppSettings.sh

.PHONY: agent-reset-settings-linux
agent-reset-settings-linux:
	@echo 'ERROR: agent-reset-settings is macOS-only.' >&2; exit 1

.PHONY: agent-reset-settings-windows
agent-reset-settings-windows:
	@throw 'agent-reset-settings is macOS-only.'

.PHONY: agent-reset-permissions
agent-reset-permissions: agent-reset-permissions-$(HOST_OS) ## Reset WendyAgentMac permissions

.PHONY: agent-reset-permissions-macos
agent-reset-permissions-macos:
	./Scripts/ResetMacAppTCC.sh

.PHONY: agent-reset-permissions-linux
agent-reset-permissions-linux:
	@echo 'ERROR: agent-reset-permissions is macOS-only.' >&2; exit 1

.PHONY: agent-reset-permissions-windows
agent-reset-permissions-windows:
	@throw 'agent-reset-permissions is macOS-only.'

.PHONY: agent-reset
agent-reset: agent-reset-$(HOST_OS) ## Reset WendyAgentMac settings and permissions

.PHONY: agent-reset-macos
agent-reset-macos: agent-reset-settings-macos agent-reset-permissions-macos

.PHONY: agent-reset-linux
agent-reset-linux:
	@echo 'ERROR: agent-reset is macOS-only.' >&2; exit 1

.PHONY: agent-reset-windows
agent-reset-windows:
	@throw 'agent-reset is macOS-only.'

.PHONY: agent-stop
agent-stop: agent-stop-$(HOST_OS) ## Quit WendyAgentMac and fail if it is still running after 10 seconds

.PHONY: agent-stop-macos
agent-stop-macos:
	./Scripts/Quit.sh

.PHONY: agent-stop-linux
agent-stop-linux:
	@echo 'ERROR: agent-stop is macOS-only.' >&2; exit 1

.PHONY: agent-stop-windows
agent-stop-windows:
	@throw 'agent-stop is macOS-only.'

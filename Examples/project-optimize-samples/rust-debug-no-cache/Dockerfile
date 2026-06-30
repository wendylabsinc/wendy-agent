# Intentionally un-optimized. See ../README.md.
# - forces amd64 on an arm64 device (QEMU / won't run)
# - cargo build without a cache mount and without --release
# - single stage, ships the full Rust toolchain
# - no .dockerignore alongside it
FROM --platform=linux/amd64 rust:1

WORKDIR /app
COPY . .
RUN cargo build
CMD ["/app/target/debug/app"]

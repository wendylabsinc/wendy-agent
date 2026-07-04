# Intentionally un-optimized. See ../README.md.
# - swift build without -c release (debug binary shipped)
# - WENDY_DEBUG is declared but never used to toggle the build
# - swift build without a cache mount
# - single stage, no .dockerignore
FROM swift:6.0

ARG WENDY_DEBUG=0
WORKDIR /app
COPY . .
RUN swift build
CMD ["/app/.build/debug/app"]

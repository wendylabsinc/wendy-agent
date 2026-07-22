# camserver has to be built via a Dockerfile rather than wendy run's Swift
# buildpack: that buildpack only auto-detects Package.swift at wendy.json's
# own directory for SINGLE-service apps (no "services" map). Multi-service
# apps (needed here so this service gets a non-empty serviceName — mesh
# egress wiring requires isolation:"isolated" AND serviceName != "", see
# go/internal/agent/containerd/client.go) always build via Docker regardless
# of any --build-type/language hint. This mirrors what the buildpack would
# have produced: build in a full Swift toolchain image, run in a slim one.
FROM swift:6.1-noble AS build
WORKDIR /build
COPY Package.swift Package.resolved* ./
COPY Sources ./Sources
RUN swift build -c release --product camserver

FROM swift:6.1-noble-slim
WORKDIR /app
COPY --from=build /build/.build/release/camserver ./camserver
CMD ["./camserver"]

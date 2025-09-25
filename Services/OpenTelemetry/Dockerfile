# Use the official contrib collector image (includes most exporters/receivers)
FROM otel/opentelemetry-collector-contrib:0.116.1

# Copy your collector configuration into the container
COPY otel-config.yaml /etc/otelcol/config.yaml

# Default command (the base image already has otelcol installed)
CMD ["--config=/etc/otelcol/config.yaml"]

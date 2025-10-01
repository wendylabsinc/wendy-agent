# OpenTelemetry Dashboard

This Docker Compose setup provides a complete OpenTelemetry observability stack with metrics visualization.

## Components

- **OpenTelemetry Collector**: Receives OTLP metrics, traces, and logs
- **Prometheus**: Stores metrics from the OTel Collector
- **Loki**: Stores logs from the OTel Collector
- **Grafana**: Provides visualization dashboards for metrics and logs

## Quick Start

```bash
# Start all services
docker-compose up -d

# View logs
docker-compose logs -f

# Stop all services
docker-compose down

# Stop and remove volumes (clears all data)
docker-compose down -v
```

## Access Points

### OpenTelemetry Collector (exposed to 0.0.0.0)
- **OTLP gRPC**: `0.0.0.0:4317`
- **OTLP HTTP**: `0.0.0.0:4318`
- **Health Check**: `http://0.0.0.0:13133`
- **ZPages**: `http://0.0.0.0:55679/debug/tracez`
- **Prometheus Exporter**: `http://0.0.0.0:8889/metrics`

### Dashboards (localhost only)
- **Grafana**: `http://localhost:3000` (admin/admin)
- **Prometheus**: `http://localhost:9090`
- **Loki**: `http://localhost:3100`

## Sending Metrics to the Collector

### Using the OTLP Endpoint

**gRPC endpoint**: `http://0.0.0.0:4317`  
**HTTP endpoint**: `http://0.0.0.0:4318`

Example with environment variables:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
```

Or for HTTP:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

## Grafana Setup

1. Open Grafana at `http://localhost:3000`
2. Login with username `admin` and password `admin`
3. Both Prometheus (metrics) and Loki (logs) are already configured as data sources
4. Create dashboards or import existing ones from [Grafana Dashboard Gallery](https://grafana.com/grafana/dashboards/)

### Viewing Logs

1. In Grafana, click **Explore** (compass icon on left sidebar)
2. Select **Loki** from the data source dropdown
3. Use LogQL to query logs:
   ```logql
   {service_name="my-service"}
   ```
4. Or browse by labels and filter by time range

### Viewing Metrics

1. In Grafana, click **Explore** or create a **Dashboard**
2. Select **Prometheus** from the data source dropdown
3. Use PromQL to query metrics:
   ```promql
   rate(otel_requests_total[5m])
   ```

### Recommended Dashboards
- OpenTelemetry Collector Dashboard: [15983](https://grafana.com/grafana/dashboards/15983)
- General Prometheus Stats: [3662](https://grafana.com/grafana/dashboards/3662)
- Loki Logs Dashboard: Create custom or use the built-in Explore view

## Troubleshooting

### Check if services are running
```bash
docker-compose ps
```

### View collector logs
```bash
docker-compose logs -f otel-collector
```

### Check collector health
```bash
curl http://localhost:13133
```

### View collector zpages
Open `http://localhost:55679/debug/servicez` in your browser to see pipeline information.

### Test metric ingestion
You can test if the collector is receiving metrics by checking the Prometheus exporter:
```bash
curl http://localhost:8889/metrics
```

## Configuration

- `otel-collector-config.yaml`: OpenTelemetry Collector configuration
- `prometheus.yml`: Prometheus scrape configuration
- `grafana-datasources.yml`: Grafana data source provisioning
- `docker-compose.yml`: Docker Compose service definitions

## Data Persistence

All data is persisted in Docker volumes:
- `prometheus-data`: Prometheus time-series database (metrics)
- `loki-data`: Loki log storage
- `grafana-data`: Grafana dashboards and settings

To reset all data:
```bash
docker-compose down -v
```

## Example LogQL Queries

Once logs are flowing to Loki, try these queries in Grafana Explore:

```logql
# All logs from a specific service
{service_name="my-service"}

# Filter by log level
{service_name="my-service"} |= "error"

# Exclude certain patterns
{service_name="my-service"} != "debug"

# Count logs per second
rate({service_name="my-service"}[1m])

# Filter by multiple labels
{service_name="my-service", level="error"}
```


FROM python:3.11-slim

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY proto /tmp/proto
RUN python -m grpc_tools.protoc \
    -I/tmp/proto \
    --python_out=/app \
    --grpc_python_out=/app \
    /tmp/proto/cloud/events.proto \
    /tmp/proto/wendy/agent/services/v1/wendy_agent_v1_event_service.proto \
    && rm -rf /tmp/proto

COPY emit_event.py .
CMD ["python", "emit_event.py"]

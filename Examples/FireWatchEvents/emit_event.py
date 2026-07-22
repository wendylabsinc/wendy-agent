#!/usr/bin/env python3
"""Emit one structured FireWatch alert through the local Wendy Agent API."""

import json
import os
import time
import uuid

import grpc

from cloud import events_pb2
from wendy.agent.services.v1 import wendy_agent_v1_event_service_pb2
from wendy.agent.services.v1 import wendy_agent_v1_event_service_pb2_grpc


def publish() -> None:
    socket = os.environ["WENDY_EVENT_SOCKET"]
    # Keep this ID stable when retrying the same detection. Cloud deduplicates it
    # within the authenticated device + app source.
    source_event_id = os.environ.get("FIREWATCH_EVENT_ID", f"fire-{uuid.uuid4()}")
    request = wendy_agent_v1_event_service_pb2.PublishAppEventRequest(
        source_event_id=source_event_id,
        title="FireWatch",
        body=os.environ.get("FIREWATCH_MESSAGE", "Potential fire detected"),
        severity=events_pb2.EVENT_SEVERITY_CRITICAL,
        target=events_pb2.EventTarget(
            live=events_pb2.LiveEventTarget(
                camera_id=os.environ.get("FIREWATCH_CAMERA_ID", "libcamera:front")
            )
        ),
    )

    with grpc.insecure_channel(f"unix://{socket}") as channel:
        client = wendy_agent_v1_event_service_pb2_grpc.WendyEventServiceStub(channel)
        for attempt in range(3):
            try:
                response = client.PublishEvent(request, timeout=10)
                print(
                    json.dumps(
                        {
                            "event_id": response.event.id,
                            "duplicate": response.duplicate,
                            "new_recipients": response.recipient_count,
                        }
                    ),
                    flush=True,
                )
                return
            except grpc.RpcError as error:
                if error.code() not in (grpc.StatusCode.UNAVAILABLE, grpc.StatusCode.ABORTED) or attempt == 2:
                    raise
                time.sleep(2**attempt)


if __name__ == "__main__":
    publish()

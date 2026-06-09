"""
HelloMultiService — API service

Part of a two-service Wendy app (api + worker).  Both services share the same
appId ("sh.wendy.examples.multiservice") but each gets its own hostname:

  WENDY_HOSTNAME       = "api.local"       # this service's mDNS name
  WENDY_APP_GROUP      = "<appId>"         # same for every service in the app
  WENDY_DEVICE_HOSTNAME = "<device>.local" # the host device's mDNS name

The worker can reach this API at http://api.local:8000.
"""

import os
import time
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI(title="HelloMultiService — API", version="1.0.0")

# --------------------------------------------------------------------------- #
# Environment identity (injected by the Wendy agent)                          #
# --------------------------------------------------------------------------- #
WENDY_HOSTNAME        = os.environ.get("WENDY_HOSTNAME", "unknown")
WENDY_APP_GROUP       = os.environ.get("WENDY_APP_GROUP", "unknown")
WENDY_APP_ID          = os.environ.get("WENDY_APP_ID", "unknown")
WENDY_DEVICE_HOSTNAME = os.environ.get("WENDY_DEVICE_HOSTNAME", "unknown")

# --------------------------------------------------------------------------- #
# Simple in-memory job queue                                                   #
# --------------------------------------------------------------------------- #
jobs: list[dict] = []


class Job(BaseModel):
    payload: str


@app.get("/")
def root():
    return {
        "service": "api",
        "wendy_hostname": WENDY_HOSTNAME,
        "wendy_app_group": WENDY_APP_GROUP,
        "wendy_app_id": WENDY_APP_ID,
        "wendy_device_hostname": WENDY_DEVICE_HOSTNAME,
        "note": "Worker reaches this service at http://api.local:8000",
    }


@app.get("/health")
def health():
    return {"status": "ok", "hostname": WENDY_HOSTNAME}


@app.post("/jobs")
def submit_job(job: Job):
    entry = {"id": len(jobs), "payload": job.payload, "submitted_at": time.time()}
    jobs.append(entry)
    return {"status": "queued", "job": entry}


@app.get("/jobs")
def list_jobs():
    return {"jobs": jobs, "count": len(jobs)}


@app.delete("/jobs/{job_id}")
def complete_job(job_id: int):
    for i, job in enumerate(jobs):
        if job["id"] == job_id:
            return {"status": "completed", "job": jobs.pop(i)}
    raise HTTPException(status_code=404, detail="job not found")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)

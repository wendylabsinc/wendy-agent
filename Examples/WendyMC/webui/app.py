import asyncio
import os
import select
import socket
import struct
from collections import deque
from pathlib import Path

from fastapi import FastAPI, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse
from fastapi.staticfiles import StaticFiles
from mcstatus import JavaServer
from pydantic import BaseModel

RCON_HOST = os.environ.get("RCON_HOST", "127.0.0.1")
RCON_PORT = int(os.environ.get("RCON_PORT", "25575"))
RCON_PASSWORD = os.environ.get("RCON_PASSWORD", "wendymc-local")
MC_HOST = os.environ.get("MC_HOST", "127.0.0.1")
MC_PORT = int(os.environ.get("MC_PORT", "25565"))
LOG_PATH = Path(os.environ.get("LOG_PATH", "/mc-data/logs/latest.log"))

STATIC_DIR = Path(__file__).parent / "static"

app = FastAPI(title="WendyMC")
app.mount("/static", StaticFiles(directory=STATIC_DIR), name="static")


@app.get("/")
async def index():
    return FileResponse(STATIC_DIR / "index.html")


@app.get("/api/status")
async def status():
    try:
        server = JavaServer(MC_HOST, MC_PORT)
        s = await server.async_status()
        sample = [p.name for p in (s.players.sample or [])]
        motd = s.description if isinstance(s.description, str) else s.motd.to_plain()
        return {
            "online": True,
            "version": s.version.name,
            "motd": motd,
            "players": {
                "online": s.players.online,
                "max": s.players.max,
                "sample": sample,
            },
            "latency_ms": round(s.latency, 1),
        }
    except Exception as e:
        return {"online": False, "reason": f"{type(e).__name__}: {e}"}


class CommandBody(BaseModel):
    cmd: str


RCON_TIMEOUT = 5.0


def _rcon_recv_exact(sock: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("RCON connection closed by server")
        buf += chunk
    return buf


def _rcon_send_packet(sock: socket.socket, req_id: int, ptype: int, payload: str) -> None:
    body = struct.pack("<ii", req_id, ptype) + payload.encode("utf-8") + b"\x00\x00"
    sock.sendall(struct.pack("<i", len(body)) + body)


def _rcon_read_packet(sock: socket.socket) -> tuple[int, int, str]:
    (length,) = struct.unpack("<i", _rcon_recv_exact(sock, 4))
    data = _rcon_recv_exact(sock, length)
    pkt_id, pkt_type = struct.unpack("<ii", data[:8])
    return pkt_id, pkt_type, data[8:-2].decode("utf-8", errors="replace")


def _rcon_send(cmd: str) -> str:
    with socket.create_connection((RCON_HOST, RCON_PORT), timeout=RCON_TIMEOUT) as sock:
        sock.settimeout(RCON_TIMEOUT)
        _rcon_send_packet(sock, 1, 3, RCON_PASSWORD)
        auth_id, _, _ = _rcon_read_packet(sock)
        if auth_id == -1:
            raise PermissionError("RCON authentication failed")
        _rcon_send_packet(sock, 2, 2, cmd)
        parts: list[str] = []
        while True:
            _, _, body = _rcon_read_packet(sock)
            parts.append(body)
            ready, _, _ = select.select([sock], [], [], 0)
            if not ready:
                return "".join(parts)


@app.post("/api/command")
async def command(body: CommandBody):
    cmd = body.cmd.strip().lstrip("/")
    if not cmd:
        raise HTTPException(status_code=400, detail="Empty command")
    try:
        response = await asyncio.to_thread(_rcon_send, cmd)
        return {"response": response}
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"RCON failed: {e}")


@app.post("/api/restart")
async def restart():
    try:
        await asyncio.to_thread(_rcon_send, "stop")
        return {"ok": True, "message": "Stop sent; container will auto-restart."}
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"RCON failed: {e}")


@app.websocket("/ws/console")
async def console(ws: WebSocket):
    await ws.accept()

    if LOG_PATH.exists():
        try:
            with open(LOG_PATH, errors="replace") as f:
                recent = deque(f, maxlen=200)
            for line in recent:
                await ws.send_text(line.rstrip("\n"))
        except OSError:
            pass

    fh = None
    inode = None
    try:
        while True:
            try:
                if fh is None:
                    fh = open(LOG_PATH, errors="replace")
                    fh.seek(0, os.SEEK_END)
                    inode = os.fstat(fh.fileno()).st_ino
                else:
                    # Detect rotation: latest.log gets recreated on MC restart
                    try:
                        if LOG_PATH.stat().st_ino != inode:
                            fh.close()
                            fh = None
                            continue
                    except FileNotFoundError:
                        fh.close()
                        fh = None
                        await asyncio.sleep(1)
                        continue
                line = fh.readline()
                if line:
                    await ws.send_text(line.rstrip("\n"))
                else:
                    await asyncio.sleep(0.5)
            except FileNotFoundError:
                if fh:
                    fh.close()
                    fh = None
                await asyncio.sleep(1)
    except WebSocketDisconnect:
        pass
    finally:
        if fh:
            fh.close()

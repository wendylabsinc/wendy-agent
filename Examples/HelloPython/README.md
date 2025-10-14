# Hello World Python HTTP Server

A simple Python HTTP server with Docker support for easy deployment.

## Features

- Simple HTTP server using Python's built-in `http.server` module
- Multiple endpoints:
  - `GET /` - HTML Hello World page
  - `GET /api/hello` - JSON API response
  - `GET /health` - Health check endpoint
  - `POST /api/echo` - Echo back posted data
- Dockerized for easy deployment
- Health check included
- Non-root user for security

## Quick Start

### Run Locally

```bash
# Run the server directly
python app.py

# Or with custom port
PORT=3000 python app.py
```

Visit `http://localhost:8000` to see the Hello World page.

### Run with Docker

```bash
# Build the Docker image
docker build -t hello-python-server .

# Run the container
docker run -p 8000:8000 hello-python-server

# Or run in background
docker run -d -p 8000:8000 --name hello-server hello-python-server
```

### Test the Endpoints

```bash
# HTML page
curl http://localhost:8000/

# JSON API
curl http://localhost:8000/api/hello

# Health check
curl http://localhost:8000/health

# Echo endpoint
curl -X POST http://localhost:8000/api/echo -d "Hello from curl"
```

## Deployment Options

### Docker Hub

```bash
# Tag for Docker Hub
docker tag hello-python-server yourusername/hello-python-server

# Push to Docker Hub
docker push yourusername/hello-python-server

# Run from Docker Hub
docker run -p 8000:8000 yourusername/hello-python-server
```

### Cloud Platforms

This containerized application can be deployed to:
- AWS ECS/Fargate
- Google Cloud Run
- Azure Container Instances
- Kubernetes
- Heroku (with container stack)
- DigitalOcean App Platform

### Environment Variables

- `PORT`: Server port (default: 8000)
- `DEBUG`: Enable debugpy for remote debugging (set to `true`)
- `DEBUG_PORT`: Debug server port (default: 5678)
- `DEBUG_WAIT`: Wait for debugger to attach before starting (set to `true`)

### Debugging

The container includes debugpy for remote debugging. To use it:

```bash
# Run with debugging enabled (doesn't wait for debugger)
docker run -e DEBUG=true -p 8000:8000 -p 5678:5678 hello-python-server

# Run with debugging and wait for debugger to attach
docker run -e DEBUG=true -e DEBUG_WAIT=true -p 8000:8000 -p 5678:5678 hello-python-server
```

Then attach your debugger to `localhost:5678`. 

**VS Code Configuration** (`.vscode/launch.json`):
```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Python: Remote Attach",
            "type": "python",
            "request": "attach",
            "connect": {
                "host": "localhost",
                "port": 5678
            },
            "pathMappings": [
                {
                    "localRoot": "${workspaceFolder}",
                    "remoteRoot": "/app"
                }
            ]
        }
    ]
}
```

## File Structure

```
.
├── app.py              # Main Python server
├── entrypoint.sh       # Docker entrypoint with debug support
├── requirements.txt    # Python dependencies
├── Dockerfile         # Docker configuration
├── .dockerignore      # Docker ignore file
└── README.md          # This file
```

## Development

The server uses Python's built-in HTTP server, so no external dependencies are required. The code is simple and easy to modify for your needs.

## Security Notes

- The Docker container runs as a non-root user
- Only necessary files are copied to the container
- Health checks are included for monitoring

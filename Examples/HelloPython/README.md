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

## File Structure

```
.
├── app.py              # Main Python server
├── requirements.txt    # Python dependencies (none required)
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

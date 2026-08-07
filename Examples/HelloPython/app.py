#!/usr/bin/env python3
"""
Simple Hello World Python HTTP Server
"""

from http.server import HTTPServer, BaseHTTPRequestHandler
import json
import os
import socket

class HelloWorldHandler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def send_body(self, status, content_type, body):
        payload = body.encode('utf-8')
        self.send_response(status)
        self.send_header('Content-type', content_type)
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.path == '/':
            html_content = """
            <!DOCTYPE html charset="utf-8">
            <html lang="en">
            <head>
                <title>Hello World Server</title>
                <style>
                    body { 
                        font-family: Arial, sans-serif; 
                        max-width: 800px; 
                        margin: 50px auto; 
                        padding: 20px;
                        background-color: #ffffff;
                    }
                    .container {
                        background-color: white;
                        padding: 30px;
                        border-radius: 10px;
                        box-shadow: 0 2px 10px rgba(0,0,0,0.1);
                        text-align: center;
                    }
                    h1 { color: #333; }
                    .status { color: #28a745; font-weight: bold; }
                </style>
            </head>
            <body>
                <div class="container">
                    <h1>Hello World</h1>
                    <p class="status">Server is running successfully!</p>
                    <p>This is a simple Python HTTP server running in a Docker container.</p>
                </div>
            </body>
            </html>
            """
            self.send_body(200, 'text/html', html_content)
            
        elif self.path == '/api/hello':
            response = {
                "message": "Hello World!",
                "status": "success",
                "server": "Python HTTP Server"
            }
            self.send_body(200, 'application/json', json.dumps(response, indent=2))
            
        elif self.path == '/health':
            health_response = {
                "status": "healthy",
                "message": "Server is running"
            }
            self.send_body(200, 'application/json', json.dumps(health_response))
            
        else:
            error_response = {
                "error": "Not Found",
                "message": f"Path {self.path} not found"
            }
            self.send_body(404, 'application/json', json.dumps(error_response))

    def do_POST(self):
        if self.path == '/api/echo':
            content_length = int(self.headers['Content-Length'])
            post_data = self.rfile.read(content_length)
            
            response = {
                "message": "Echo endpoint",
                "received_data": post_data.decode('utf-8'),
                "status": "success"
            }
            self.send_body(200, 'application/json', json.dumps(response, indent=2))
        else:
            error_response = {
                "error": "Not Found",
                "message": f"POST endpoint {self.path} not found"
            }
            self.send_body(404, 'application/json', json.dumps(error_response))

    def log_message(self, format, *args):
        print(f"[{self.date_time_string()}] {format % args}", flush=True)

def run_server(port=8000):
    server_address = ('0.0.0.0', port)
    httpd = HTTPServer(server_address, HelloWorldHandler)
    print(f"Starting server on {server_address[0]}:{server_address[1]}", flush=True)
    print(f"Visit http://localhost:{port} to see the Hello World page", flush=True)
    print(f"API endpoints available:", flush=True)
    print(f"  GET  /api/hello - JSON hello message", flush=True)
    print(f"  GET  /health - Health check", flush=True)
    print(f"  POST /api/echo - Echo back posted data", flush=True)
    print("Server is ready to accept connections...", flush=True)
    
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down server...", flush=True)
        httpd.shutdown()

if __name__ == '__main__':
    port = int(os.environ.get('PORT', 8000))
    run_server(port)

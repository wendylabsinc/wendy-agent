#!/bin/bash

# Entrypoint script for running the app with optional debugpy

exec python -m debugpy --listen "0.0.0.0:5678" app.py
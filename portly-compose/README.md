# Portly Docker Compose

Example Docker Compose setup for running portly with persistent storage.

## One-Line Install

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/greatbody/portly/main/portly-compose/install.sh)
```

This downloads `compose.yaml` and `config.example.yaml`, then starts Portly immediately.

## Manual Quick Start

1. Copy your `config.yaml` to this directory:
   ```bash
   cp ../config.yaml ./config.yaml
   ```

2. Start the service:
   ```bash
   docker compose up -d
   ```

3. Access the web interface:
   ```
   http://localhost:8080
   ```

## Services

- **portly**: The reverse proxy service listening on port 8080

## Volumes

- **portly-data**: Named volume for persistent SQLite database storage

## Configuration

Edit `config.yaml` to customize:
- Listen address and port
- Admin password
- Proxy targets
- TLS settings (if using an external reverse proxy)

For more details, see the main [README.md](../README.md).

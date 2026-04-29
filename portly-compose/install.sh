#!/bin/bash
set -e

# Portly one-line installer
# Run with: bash <(curl -fsSL https://raw.githubusercontent.com/greatbody/portly/main/portly-compose/install.sh)

PORTLY_DIR="${PORTLY_DIR:-.}"
COMPOSE_FILE="$PORTLY_DIR/compose.yaml"
CONFIG_FILE="$PORTLY_DIR/config.yaml"

echo "Installing Portly..."

# Create directory if it doesn't exist
mkdir -p "$PORTLY_DIR"

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    echo "❌ Docker is not installed. Please install Docker first."
    exit 1
fi

# Check if docker compose is available
if ! docker compose version &> /dev/null; then
    echo "❌ Docker Compose is not installed. Please install Docker Compose first."
    exit 1
fi

# Download compose.yaml
echo "📥 Downloading compose.yaml..."
curl -fsSL https://raw.githubusercontent.com/greatbody/portly/main/portly-compose/compose.yaml -o "$COMPOSE_FILE"

# Download config.example.yaml if config.yaml doesn't exist
if [ ! -f "$CONFIG_FILE" ]; then
    echo "📥 Downloading config.example.yaml..."
    curl -fsSL https://raw.githubusercontent.com/greatbody/portly/main/portly-compose/config.example.yaml -o "$CONFIG_FILE"
    echo "⚠️  Edit $CONFIG_FILE to customize settings before running."
fi

# Start the service
echo "🚀 Starting Portly..."
cd "$PORTLY_DIR"
docker compose up -d

echo ""
echo "✅ Portly installed and started!"
echo ""
echo "Access the web interface at: http://localhost:8080"
echo "Config file: $CONFIG_FILE"
echo "View logs: docker compose logs -f"
echo ""
echo "To stop: docker compose down"

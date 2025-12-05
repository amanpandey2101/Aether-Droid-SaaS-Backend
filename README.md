# Aether Droid - Android Cloud Emulator Backend

A cloud-based Android emulator platform that streams Android devices to web browsers via WebRTC. Built with Go, Docker, and MongoDB.

![Go Version](https://img.shields.io/badge/Go-1.21+-blue)
![License](https://img.shields.io/badge/License-MIT-green)

## 🚀 Features

- **Cloud Android Emulators** - Run Android emulators in Docker containers
- **WebRTC Streaming** - Low-latency video streaming to web browsers
- **Touch & Keyboard Input** - Full interaction support via WebRTC DataChannels
- **Multi-user Support** - JWT-based authentication with per-user containers
- **Container Management** - Create, start, stop, and delete emulator instances
- **H.264 Encoding** - Optimized software encoding for cloud VMs

## 📋 Prerequisites

- **Go** 1.21 or later
- **Docker** with Docker daemon running
- **MongoDB** 4.4 or later
- **FFmpeg** with libx264 support
- **KVM** support (for Android emulator acceleration)

### Linux-specific Requirements

```bash
# Enable KVM
sudo modprobe kvm
sudo modprobe kvm_intel  # or kvm_amd

# Add user to docker and kvm groups
sudo usermod -aG docker,kvm $USER
```

## 🛠️ Installation

### 1. Clone the Repository

```bash
git clone https://github.com/yourusername/Aether-Droid-SaaS-Backend.git
cd Aether-Droid-SaaS-Backend
```

### 2. Install Dependencies

```bash
go mod download
```

### 3. Configure Environment Variables

Create a `.env` file in the project root:

```bash
cp .env.example .env
```

Edit the `.env` file with your configuration (see [Environment Variables](#-environment-variables) section).

### 4. Pull Android Emulator Image

```bash
docker pull us-docker.pkg.dev/android-emulator-268719/images/30-google-x64:30.1.2
```

### 5. Run the Server

```bash
go run cmd/server/main.go
```

## ⚙️ Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| **Server** | | |
| `SERVER_PORT` | HTTP server port | `8080` |
| **Database** | | |
| `DATABASE_URL` | MongoDB connection string | *(required)* |
| **Docker** | | |
| `DOCKER_HOST` | Docker daemon socket | `unix:///var/run/docker.sock` |
| `EMULATOR_REGISTRY` | Container registry for emulator images | `us-docker.pkg.dev/android-emulator-268719/images` |
| `DOCKER_NETWORK` | Docker network name | `aether-network` |
| **Emulator** | | |
| `EMULATOR_ADDR` | Default emulator gRPC address | `localhost:8554` |
| `EMULATOR_GRPC_PORT` | Default gRPC port for emulators | `8554` |
| `EMULATOR_ADB_PORT` | Default ADB port for emulators | `5555` |
| `EMULATOR_MEMORY` | Memory allocation per emulator | `8g` |
| `EMULATOR_CPUS` | CPU cores per emulator | `3` |
| `ADB_KEY_PATH` | Path to ADB private key | `~/.android/adbkey` |
| **WebRTC** | | |
| `WEBRTC_MIN_PORT` | Minimum UDP port for WebRTC | `50000` |
| `WEBRTC_MAX_PORT` | Maximum UDP port for WebRTC | `60000` |
| `TURN_SERVER` | TURN server URL | `turn:turn.aether.dev:3478` |
| `TURN_USER` | TURN server username | `dev` |
| `TURN_PASS` | TURN server password | `dev123` |
| **Streaming** | | |
| `STREAM_WIDTH` | Video stream width | `360` |
| `STREAM_HEIGHT` | Video stream height | `640` |

### Example `.env` File

```env
# Server
SERVER_PORT=3001

# Database
DATABASE_URL=mongodb://localhost:27017/aether

# Docker
DOCKER_HOST=unix:///var/run/docker.sock
EMULATOR_REGISTRY=us-docker.pkg.dev/android-emulator-268719/images

# Emulator
EMULATOR_MEMORY=8g
EMULATOR_CPUS=3

# TURN Server (for NAT traversal)
TURN_SERVER=turn:your-server.com:3478
TURN_USER=username
TURN_PASS=password
```

## 📡 API Reference

All API endpoints require JWT authentication (except health check).

### Authentication

Include JWT token in the `Authorization` header:
```
Authorization: Bearer <your-jwt-token>
```

### Health Check

```http
GET /health
```

**Response:**
```json
{
  "status": "healthy",
  "version": "1.0.0"
}
```

---

### List Available Images

```http
GET /api/images
```

**Response:**
```json
{
  "images": [
    {
      "id": "30-google-x64",
      "name": "Android 11 (Google APIs) ⭐",
      "api_level": 30,
      "android_version": "11",
      "variant": "google",
      "abi": "x86_64",
      "tag": "30.1.2",
      "full_image": "us-docker.pkg.dev/.../30-google-x64:30.1.2"
    }
  ]
}
```

---

### List Containers

```http
GET /api/containers
```

**Response:**
```json
{
  "containers": [
    {
      "id": "abc123...",
      "name": "my-emulator",
      "image_id": "30-google-x64",
      "status": "running",
      "grpc_port": 10000,
      "adb_port": 10001,
      "created_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

---

### Create Container

```http
POST /api/containers
Content-Type: application/json

{
  "name": "my-emulator",
  "image_id": "30-google-x64"
}
```

**Response:**
```json
{
  "id": "abc123...",
  "name": "my-emulator",
  "image_id": "30-google-x64",
  "status": "running",
  "grpc_port": 10000,
  "adb_port": 10001
}
```

---

### Get Container Details

```http
GET /api/containers/{container_id}
```

---

### Start Container

```http
POST /api/containers/{container_id}/start
```

---

### Stop Container

```http
POST /api/containers/{container_id}/stop
```

---

### Delete Container

```http
DELETE /api/containers/{container_id}
```

---

### WebRTC Offer (Specific Container)

```http
POST /offer/{container_id}
Content-Type: application/json

{
  "type": "offer",
  "sdp": "v=0\r\no=- ..."
}
```

**Response:**
```json
{
  "type": "answer",
  "sdp": "v=0\r\no=- ..."
}
```

## 🎮 WebRTC Data Channel Commands

The WebRTC connection includes a DataChannel named `input` for sending user interactions:

### Touch Input
```
touch:{x}:{y}
```
Example: `touch:180:320`

### Keyboard Input
```
keydown:{key}:{code}
keyup:{key}:{code}
```
Example: `keydown:a:KeyA`

### Swipe Gesture
```
swipe:{startX}:{startY}:{endX}:{endY}:{durationMs}
```
Example: `swipe:100:500:100:200:300`

### Special Keys
- `key:GoHome` - Home button
- `key:GoBack` - Back button
- `key:Power` - Power button
- `key:AppSwitch` - Recent apps

## 🏗️ Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   Web Browser   │────▶│   Go Backend     │────▶│  Docker/KVM     │
│   (WebRTC)      │◀────│   (Signaling)    │◀────│  (Emulator)     │
└─────────────────┘     └──────────────────┘     └─────────────────┘
                               │
                               ▼
                        ┌──────────────────┐
                        │    MongoDB       │
                        │   (User Data)    │
                        └──────────────────┘
```

### Components

1. **Web Browser** - Renders video stream, sends touch/keyboard input
2. **Go Backend** - WebRTC signaling, container management, API server
3. **Docker/KVM** - Runs Android emulator containers
4. **MongoDB** - Stores user data and container metadata

## 🔧 Development

### Project Structure

```
.
├── cmd/
│   └── server/
│       └── main.go           # Application entry point
├── internal/
│   ├── api/
│   │   ├── handler.go        # HTTP handlers
│   │   ├── router.go         # Route definitions
│   │   └── middleware.go     # JWT, CORS, logging
│   ├── config/
│   │   └── config.go         # Configuration loading
│   ├── container/
│   │   ├── docker.go         # Docker client wrapper
│   │   └── types.go          # Container types, image list
│   ├── database/
│   │   └── database.go       # MongoDB service
│   ├── emulator/
│   │   ├── client.go         # gRPC client for emulator
│   │   ├── h264_encoder.go   # FFmpeg H.264 encoding
│   │   ├── broadcaster.go    # WebRTC media broadcaster
│   │   └── frame_stream.go   # Frame streaming
│   └── models/
│       └── models.go         # Data models
├── .env                      # Environment variables
└── go.mod                    # Go module definition
```

### Building

```bash
# Build binary
go build -o aether-backend cmd/server/main.go

# Run with env file
./aether-backend
```

### Testing

```bash
# Run tests
go test ./...

# Run with coverage
go test -cover ./...
```

## 🐳 Docker Deployment

### Using Docker Compose

```yaml
version: '3.8'
services:
  backend:
    build: .
    ports:
      - "3001:3001"
    environment:
      - DATABASE_URL=mongodb://mongo:27017/aether
      - DOCKER_HOST=unix:///var/run/docker.sock
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ~/.android:/root/.android
    depends_on:
      - mongo
      
  mongo:
    image: mongo:6
    volumes:
      - mongo-data:/data/db

volumes:
  mongo-data:
```

## 📄 License

MIT License - see [LICENSE](LICENSE) for details.

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## 📞 Support

For issues and questions, please open a GitHub issue.

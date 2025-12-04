# GPU Bridge Streaming Architecture

This package implements the new GPU-accelerated video streaming pipeline for the Android cloud emulator.

## Architecture

### OLD (Deprecated):
```
Emulator → gRPC GetFrame → Go Server → RGBA → H264Encoder → WebRTC
```
**Problems:**
- High CPU usage for pixel processing
- High memory for RGBA buffers
- Network overhead for gRPC frame transfer
- Software encoding bottleneck

### NEW (GPU Bridge):
```
Emulator → virtio-gpu → ffmpeg (VAAPI/NVENC) → H.264 NAL → WebRTC → Browser
```
**Benefits:**
- Zero-copy GPU rendering
- Hardware video encoding
- No pixel processing in Go
- Minimal CPU usage
- Lower latency

## Package Structure

```
internal/bridge/
├── bridge.go           # Local bridge (for testing)
├── container_bridge.go # Docker container bridge (production)
└── README.md           # This file
```

## Key Components

### ContainerBridge
Manages streaming from inside Docker containers:
1. Creates Docker exec with ffmpeg command
2. Captures from DRM/KMS framebuffer (kmsgrab)
3. Encodes using VAAPI or NVENC
4. Streams H.264 to WebRTC track

### Encoder Fallback Chain
1. **VAAPI** (Intel/AMD) - Preferred for WSL2
2. **NVENC** (NVIDIA) - If NVIDIA Container Toolkit available
3. **x264** (Software) - Fallback for testing

## Container Requirements

### Required Device Mappings
```yaml
devices:
  - /dev/kvm:/dev/kvm
  - /dev/dri/renderD128:/dev/dri/renderD128
  - /dev/dri/card0:/dev/dri/card0
```

### Required Environment
```yaml
environment:
  - ANDROID_EMU_GPU=virtio
  - ANDROID_EMU_FEATURES=allow-host-graphics
  - DISPLAY=:0
```

### Required Host Config
```yaml
privileged: true
cap_add:
  - SYS_ADMIN
security_opt:
  - seccomp=unconfined
```

### For NVIDIA GPUs
```yaml
device_requests:
  - capabilities: [[gpu]]
    count: -1
```

## Usage

### Starting the Bridge
The bridge is automatically started when a WebRTC connection is established:

```go
// In WebRTC connection state handler
if state == webrtc.PeerConnectionStateConnected {
    bridgeCfg := &bridge.ContainerBridgeConfig{
        ContainerID: containerID,
        DockerHost:  containerManager.GetDockerHost(),
        HTTPClient:  containerManager.GetHTTPClient(),
        FPS:         30,
        Width:       1280,
        Height:      720,
    }
    
    containerBridge := bridge.NewContainerBridge(videoTrack, bridgeCfg)
    err := containerBridge.Start(ctx)
}
```

## Testing in WSL2

### 1. Verify GPU Support
```bash
# Check for DRI devices
ls -la /dev/dri/

# Should show:
# renderD128 (render node)
# card0 (GPU device)

# Test VAAPI
vainfo --display drm --device /dev/dri/renderD128
```

### 2. Test Container GPU Access
```bash
docker run --rm -it \
    --privileged \
    --device /dev/kvm \
    --device /dev/dri/card0 \
    --device /dev/dri/renderD128 \
    -e ANDROID_EMU_GPU=virtio \
    -e ANDROID_EMU_FEATURES=allow-host-graphics \
    emulator-image:latest /bin/bash

# Inside container:
vainfo
ffmpeg -hwaccel vaapi -hwaccel_device /dev/dri/renderD128 -f testsrc2 -t 1 -f null -
```

### 3. Test Hardware Encoding
```bash
# VAAPI test
ffmpeg -hwaccel vaapi -hwaccel_device /dev/dri/renderD128 \
    -f testsrc2 -t 5 -pix_fmt yuv420p \
    -vf 'format=nv12,hwupload' \
    -c:v h264_vaapi -f null -

# Should complete without errors
```

## Troubleshooting

### "No render device found"
- WSL2 may not have GPU passthrough configured
- Install Windows GPU drivers with WSL2 support
- Ensure `/dev/dri/renderD128` exists in WSL2

### "VAAPI init failed"
- Install mesa-va-drivers in container
- Check libva configuration
- Verify DRM device permissions

### "Permission denied on /dev/dri"
- Container needs privileged mode
- Or add user to `video` and `render` groups

### Bridge Falls Back to gRPC
- Check container logs for ffmpeg errors
- Verify devices are properly mapped
- Check that emulator uses virtio-gpu

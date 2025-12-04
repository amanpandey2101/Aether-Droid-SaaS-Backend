---
description: Test GPU bridge streaming in WSL2
---

# GPU Bridge Testing in WSL2

This workflow tests the new GPU-accelerated streaming architecture.

## Prerequisites

1. WSL2 Ubuntu installed
2. Docker Desktop with WSL2 backend
3. GPU drivers installed (Intel/AMD/NVIDIA with WSL2 support)

## Steps

// turbo-all

### 1. Verify WSL2 GPU Support

```bash
# Check for DRI devices
ls -la /dev/dri/
```

Expected output should include `renderD128` and `card0`.

### 2. Install VAAPI Tools (if needed)

```bash
sudo apt update
sudo apt install -y vainfo mesa-va-drivers libva-dev
```

### 3. Test VAAPI

```bash
vainfo --display drm --device /dev/dri/renderD128
```

### 4. Run Test Container with GPU

```bash
docker run --rm -it \
    --privileged \
    --device /dev/kvm \
    --device /dev/dri/card0 \
    --device /dev/dri/renderD128 \
    -e ANDROID_EMU_GPU=virtio \
    -e ANDROID_EMU_FEATURES=allow-host-graphics \
    ubuntu:latest bash
```

### 5. Inside Container - Install FFmpeg

```bash
apt update && apt install -y ffmpeg vainfo libva-drm2 libdrm2
```

### 6. Test Hardware Encoding

```bash
# Test VAAPI encoder
ffmpeg -hwaccel vaapi -hwaccel_device /dev/dri/renderD128 \
    -f testsrc2 -t 3 -pix_fmt yuv420p \
    -vf 'format=nv12,hwupload' \
    -c:v h264_vaapi -f null -
```

### 7. Run Backend with GPU Bridge

```bash
cd aether-go-backend
go run ./cmd/server
```

Look for:
- `🎬 GPU bridge streaming enabled`
- When connecting: `🚀 Starting GPU bridge for container`
- Success: `✅ GPU bridge started for XXX - Go is NOT processing pixels`

### 8. Verify No Pixel Processing

Check logs - you should NOT see:
- `Received frame: WxH`
- `H264 encoder produced X bytes`
- `GetFrame`

## Troubleshooting

### No /dev/dri devices
- Ensure Windows GPU drivers support WSL2g
- Restart WSL2: `wsl --shutdown` then reopen

### Container GPU access denied
- Use `--privileged` flag
- Or add specific device access with `--device`

### VAAPI not available
- Install mesa-va-drivers
- Check with `vainfo`

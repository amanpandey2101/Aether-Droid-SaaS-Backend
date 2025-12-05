// DEPRECATED: frame_stream.go
// This file contains the old frame streaming logic (GetFrame, StreamScreenshot).
// These functions are replaced by the GPU bridge architecture (internal/bridge/).
//
// The new architecture:
// - Emulator renders to virtio-gpu framebuffer
// - ffmpeg inside container captures via kmsgrab/DRM
// - Hardware encoding via VAAPI/NVENC
// - H.264 NALs sent directly to WebRTC
//
// Go server no longer touches raw frames - only handles WebRTC signaling.
// This file is kept for backward compatibility and fallback scenarios.
//
// TODO: Remove after GPU bridge is fully validated
package emulator

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"strconv"
	"time"

	pb "android_cloud_backend/internal/emulator/proto"
)

// StreamConfig holds configuration for frame streaming
type StreamConfig struct {
	Width  uint32 // Request this width from emulator (0 = native)
	Height uint32 // Request this height from emulator (0 = native)
	UseRGB bool   // Use RGB888 instead of RGBA8888 (25% smaller)
}

// DefaultStreamConfig returns optimized settings for cloud VMs
func DefaultStreamConfig() *StreamConfig {
	// Read from environment or use defaults
	// 360p for ultra-fast software encoding on cloud VMs
	width := uint32(360)  // 360p width
	height := uint32(640) // Portrait mode

	if w := os.Getenv("STREAM_WIDTH"); w != "" {
		if v, err := strconv.ParseUint(w, 10, 32); err == nil {
			width = uint32(v)
		}
	}
	if h := os.Getenv("STREAM_HEIGHT"); h != "" {
		if v, err := strconv.ParseUint(h, 10, 32); err == nil {
			height = uint32(v)
		}
	}

	return &StreamConfig{
		Width:  width,
		Height: height,
		UseRGB: false, // RGBA is needed for proper image.RGBA conversion
	}
}

// ensureStream creates or recreates the stream if needed
// Optimized for GCP/cloud VMs: requests lower resolution to reduce bandwidth
func (e *EmulatorClient) ensureStream() error {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()

	if e.stream != nil {
		return nil // Stream already exists
	}

	// Get optimized stream settings
	cfg := DefaultStreamConfig()

	// Request RGBA8888 format at reduced resolution for cloud VMs
	// Lower resolution = less data to transfer = faster encoding
	format := &pb.ImageFormat{
		Format: pb.ImageFormat_RGBA8888,
		Width:  cfg.Width,  // Request scaled down
		Height: cfg.Height, // Request scaled down
	}

	log.Printf("📺 Creating screenshot stream: %dx%d RGBA8888", cfg.Width, cfg.Height)

	stream, err := e.client.StreamScreenshot(context.Background(), format)
	if err != nil {
		return fmt.Errorf("failed to create StreamScreenshot: %w", err)
	}

	e.stream = stream
	log.Printf("✅ Screenshot stream created")
	return nil
}

// closeStream closes the current stream
func (e *EmulatorClient) closeStream() {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()

	if e.stream != nil {
		log.Printf(" Closing screenshot stream")
		// Note: gRPC streams don't have explicit Close methods
		// The stream will be closed when the context is cancelled or connection drops
		e.stream = nil
	}
}

func (e *EmulatorClient) GetFrame() (image.Image, error) {
	// Get optimized stream settings
	cfg := DefaultStreamConfig()

	// Use GetScreenshot (polling) instead of StreamScreenshot
	// This actively requests a frame instead of waiting for screen changes
	format := &pb.ImageFormat{
		Format: pb.ImageFormat_RGBA8888,
		Width:  cfg.Width,
		Height: cfg.Height,
	}

	// Call GetScreenshot with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := e.client.GetScreenshot(ctx, format)
	if err != nil {
		return nil, fmt.Errorf("GetScreenshot failed: %w", err)
	}

	w := int(res.Format.Width)
	h := int(res.Format.Height)

	if w == 0 || h == 0 {
		return nil, fmt.Errorf("invalid resolution: %dx%d", w, h)
	}

	// Validate frame data size
	expectedSize := w * h * 4
	actualSize := len(res.Image)

	if actualSize < expectedSize {
		return nil, fmt.Errorf("frame data too small: expected %d, got %d", expectedSize, actualSize)
	}

	// Create output RGBA image directly from raw bytes
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, res.Image[:expectedSize])

	return img, nil
}

// GetRawFrame returns the raw frame bytes without creating an image object
// This avoids memory allocation and copying overhead
func (e *EmulatorClient) GetRawFrame() ([]byte, int, int, error) {
	cfg := DefaultStreamConfig()
	format := &pb.ImageFormat{
		Format: pb.ImageFormat_RGBA8888,
		Width:  cfg.Width,
		Height: cfg.Height,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	res, err := e.client.GetScreenshot(ctx, format)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("GetScreenshot failed: %w", err)
	}

	w := int(res.Format.Width)
	h := int(res.Format.Height)

	return res.Image, w, h, nil
}

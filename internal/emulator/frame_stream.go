package emulator

import (
	"context"
	"fmt"
	"image"
	"log"
	"time"

	pb "android_cloud_backend/internal/emulator/proto"
)

// ensureStream creates or recreates the stream if needed
func (e *EmulatorClient) ensureStream() error {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()

	if e.stream != nil {
		return nil // Stream already exists
	}

	// Create new streaming RPC with raw RGBA8888 format for fastest performance
	stream, err := e.client.StreamScreenshot(context.Background(), &pb.ImageFormat{
		Format: pb.ImageFormat_RGBA8888,
	})
	if err != nil {
		return fmt.Errorf("failed to create StreamScreenshot: %w", err)
	}

	e.stream = stream
	log.Printf("📡 Created new screenshot stream")
	return nil
}

// closeStream closes the current stream
func (e *EmulatorClient) closeStream() {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()

	if e.stream != nil {
		log.Printf("🔌 Closing screenshot stream")
		// Note: gRPC streams don't have explicit Close methods
		// The stream will be closed when the context is cancelled or connection drops
		e.stream = nil
	}
}

func (e *EmulatorClient) GetFrame() (image.Image, error) {
	// Ensure we have an active stream
	if err := e.ensureStream(); err != nil {
		return nil, fmt.Errorf("ensureStream failed: %w", err)
	}

	e.streamMu.Lock()
	stream := e.stream
	e.streamMu.Unlock()

	if stream == nil {
		return nil, fmt.Errorf("stream is nil after ensure")
	}

	// Log stream receive attempt (every 2 seconds to avoid spam)
	// This will help debug if GetFrame is hanging
	defer func() {
		// This will run when GetFrame returns, helping us see if it hangs
	}()

	// Receive ONE frame from the persistent stream with timeout
	frameCh := make(chan *pb.Image, 1)
	errCh := make(chan error, 1)

	go func() {
		res, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		frameCh <- res
	}()

	var res *pb.Image
	select {
	case res = <-frameCh:
		// Frame received successfully
	case err := <-errCh:
		// If stream fails, close it so next call will create a new one
		e.closeStream()
		return nil, fmt.Errorf("stream.Recv failed: %w", err)
	case <-time.After(10 * time.Second):
		// Timeout - the emulator might be busy, close stream and let next call create new one
		log.Printf("⏰ Stream recv timeout after 10s - recreating stream")
		e.closeStream()
		return nil, fmt.Errorf("stream.Recv timeout after 10 seconds")
	}

	w := int(res.Format.Width)
	h := int(res.Format.Height)

	if w == 0 || h == 0 {
		return nil, fmt.Errorf("invalid resolution: %dx%d", w, h)
	}

	// Validate frame data size (should be width * height * 4 for RGBA8888)
	expectedSize := w * h * 4
	actualSize := len(res.Image)

	if actualSize < expectedSize {
		return nil, fmt.Errorf("frame data too small: expected %d bytes, got %d", expectedSize, actualSize)
	}

	// Warn if frame is much larger than expected (possible corruption)
	if actualSize > expectedSize*2 {
		log.Printf("⚠️ Frame data larger than expected: expected ~%d bytes, got %d bytes", expectedSize, actualSize)
	}

	// Create output RGBA image directly from raw bytes (fastest)
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	// Only copy the expected amount of data to prevent buffer overflows
	copySize := expectedSize
	if actualSize < copySize {
		copySize = actualSize
	}
	copy(img.Pix, res.Image[:copySize])

	log.Printf("📺 Received frame: %dx%d (%d bytes)", w, h, actualSize)
	return img, nil
}

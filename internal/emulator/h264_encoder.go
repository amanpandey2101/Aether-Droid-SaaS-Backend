// Package emulator contains emulator client and encoding utilities
//
// DEPRECATED: h264_encoder.go
// This file is deprecated and scheduled for removal.
// The new GPU bridge architecture (internal/bridge/) handles video encoding
// directly inside containers using VAAPI/NVENC hardware acceleration.
//
// Migration path:
// - Go server no longer processes raw frames
// - Encoding happens inside container via ffmpeg with hardware acceleration
// - This file is kept for backward compatibility only
//
// TODO: Remove this file after full migration to GPU bridge
package emulator

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type H264Encoder struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdinBuf  *bufio.Writer
	stdout    io.ReadCloser
	width     int
	height    int
	fps       int
	mu        sync.Mutex
	outputBuf chan []byte
	closed    bool
	frameNum  int64
	busy      int32
}

func (e *H264Encoder) Output() <-chan []byte {
	return e.outputBuf
}

func (e *H264Encoder) FPS() int {
	return e.fps
}

func (e *H264Encoder) IsBusy() bool {
	return atomic.LoadInt32(&e.busy) == 1
}

// NewH264Encoder creates a new H264 encoder optimized for the current environment
// On GPU instances: Uses NVENC for hardware encoding
// On cloud VMs without GPU: Uses optimized x264 software encoding
func NewH264Encoder(width, height int, fps int) (*H264Encoder, error) {
	// Check if GPU is available by checking for /dev/dri or NVIDIA
	if hasGPU() {
		log.Println("🎮 GPU detected, using NVENC hardware encoder")
		enc, err := newNVENCEncoder(width, height, fps)
		if err == nil {
			return enc, nil
		}
		log.Printf("⚠️ NVENC failed, falling back to x264: %v", err)
	} else {
		log.Println("💻 No GPU detected, using optimized x264 software encoder")
	}

	return newCloudOptimizedX264Encoder(width, height, fps)
}

// hasGPU checks if GPU acceleration is available
func hasGPU() bool {
	// Check for NVIDIA GPU
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return true
	}
	// Check for Intel/AMD GPU (DRI)
	if _, err := os.Stat("/dev/dri/renderD128"); err == nil {
		return true
	}
	return false
}

// newNVENCEncoder creates an NVIDIA hardware-accelerated encoder
func newNVENCEncoder(width, height int, fps int) (*H264Encoder, error) {

	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",

		// INPUT settings
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",

		// FIX: CPU convert RGBA -> YUV420P (required)
		"-vf", "format=yuv420p,hwupload_cuda",

		// NVENC (FULL GPU ENCODER)
		"-c:v", "h264_nvenc",
		"-preset", "p1", // ultra-fast
		"-tune", "ull", // ultra-low latency
		"-rc", "constqp", // maximum GPU usage
		"-qp", "22", // increase quality by lowering number
		"-g", strconv.Itoa(fps),
		"-keyint_min", "1",
		"-profile:v", "high",
		"-level", "5.1",

		// Output
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	)

	return setupEncoder(cmd, width, height, fps)
}

// newX264Encoder creates a software x264 encoder (fallback)
func newX264Encoder(width, height int, fps int) (*H264Encoder, error) {
	// x264 with ultra-low latency settings
	cmd := exec.Command("ffmpeg",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
		"-an",
		// x264 software encoder
		"-c:v", "libx264",
		"-preset", "ultrafast", // Fastest preset
		"-tune", "zerolatency", // Zero latency tuning
		"-crf", "23", // Quality level
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", fps),
		"-keyint_min", "1",
		"-profile:v", "baseline",
		"-level", "4.2",
		"-x264-params", "keyint=10:min-keyint=10:no-scenecut",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"-flags", "+cgop", // Ensure SPS/PPS are repeated
		"-flush_packets", "1",
		"pipe:1",
	)

	return setupEncoder(cmd, width, height, fps)
}

// newCloudOptimizedX264Encoder creates a software x264 encoder optimized for cloud VMs
// Settings tuned for GCP/AWS instances: lower CPU usage, bitrate limiting, faster encoding
func newCloudOptimizedX264Encoder(width, height int, fps int) (*H264Encoder, error) {
	// Limit FPS on cloud to reduce CPU load
	targetFPS := fps
	if targetFPS > 20 {
		targetFPS = 20
		log.Printf("💡 Limiting FPS to %d for cloud VM optimization", targetFPS)
	}

	// Use superfast instead of ultrafast - better quality at similar speed
	// Use higher CRF (lower quality) to reduce encoding CPU
	// Add bitrate limit to prevent bandwidth spikes
	cmd := exec.Command("ffmpeg",
		"-loglevel", "warning",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(targetFPS),
		"-i", "pipe:0",
		"-an",
		// Optimized x264 for cloud
		"-c:v", "libx264",
		"-preset", "superfast", // Good balance of speed/quality
		"-tune", "zerolatency", // Essential for streaming
		"-crf", "28", // Higher = faster encoding, smaller files
		"-maxrate", "1500k", // Cap bitrate for consistent streaming
		"-bufsize", "3000k", // Buffer for rate control
		"-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(targetFPS), // Keyframe every second
		"-keyint_min", "1",
		"-profile:v", "baseline", // Maximum compatibility
		"-level", "3.1", // Lower level = faster decode on client
		"-threads", "4", // Limit threads to not starve other processes
		"-thread_type", "slice", // Slice-based threading for low latency
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"-fflags", "nobuffer", // Minimize buffering
		"-flags", "low_delay", // Low delay mode
		"-flush_packets", "1",
		"pipe:1",
	)

	return setupEncoder(cmd, width, height, fps)
}

// setupEncoder initializes the encoder process
func setupEncoder(cmd *exec.Cmd, width, height, fps int) (*H264Encoder, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	// Use buffered writer for stdin to prevent broken pipe issues
	stdinBuf := bufio.NewWriterSize(stdin, 4*1024*1024)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Log stderr for debugging
	stderr, _ := cmd.StderrPipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				log.Printf(" FFmpeg: %s", string(buf[:n]))
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	enc := &H264Encoder{
		cmd:       cmd,
		stdin:     stdin,
		stdinBuf:  stdinBuf,
		stdout:    stdout,
		width:     width,
		height:    height,
		fps:       fps,
		outputBuf: make(chan []byte, 20), // Small buffer for low latency
		closed:    false,
	}

	// Start background reader goroutine
	go enc.readLoop()

	return enc, nil
}

// readLoop continuously reads from ffmpeg stdout
func (e *H264Encoder) readLoop() {
	readBuf := make([]byte, 128*1024) // 128KB read buffer
	totalBytes := 0
	for {
		n, err := e.stdout.Read(readBuf)
		if err != nil {
			log.Printf(" H264 encoder read loop ended: %v (total bytes read: %d)", err, totalBytes)
			close(e.outputBuf)
			return
		}
		if n > 0 {
			totalBytes += n
			log.Printf(" H264 encoder produced %d bytes (total: %d)", n, totalBytes)

			// Copy the data since we're reusing the buffer
			data := make([]byte, n)
			copy(data, readBuf[:n])

			// Non-blocking send - drop old frames if buffer is full to keep latency low
			select {
			case e.outputBuf <- data:
				log.Printf(" H264 data queued for sending")
			default:
				// Buffer full - drain oldest and add new
				log.Printf(" H264 buffer full, dropping old frame")
				select {
				case <-e.outputBuf:
				default:
				}
				select {
				case e.outputBuf <- data:
				default:
				}
			}
		}
	}
}

// Encode sends an RGBA frame to ffmpeg and reads back H.264 NAL units
// Optimized for minimal latency
func (e *H264Encoder) Encode(img image.Image) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil, fmt.Errorf("encoder is closed")
	}

	// Set busy flag
	atomic.StoreInt32(&e.busy, 1)
	defer atomic.StoreInt32(&e.busy, 0)

	// Convert image.Image → RGBA byte slice
	rgba, ok := img.(*image.RGBA)
	if !ok {
		return nil, fmt.Errorf("image is not RGBA")
	}

	// Send raw RGBA pixels into ffmpeg using buffered writer
	_, err := e.stdinBuf.Write(rgba.Pix)
	if err != nil {
		return nil, fmt.Errorf("write to ffmpeg: %w", err)
	}

	// Flush the buffer to ensure data is sent
	if err := e.stdinBuf.Flush(); err != nil {
		return nil, fmt.Errorf("flush to ffmpeg: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("write to ffmpeg: %w", err)
	}

	// Flush the buffer to ensure data is sent
	if err := e.stdinBuf.Flush(); err != nil {
		return nil, fmt.Errorf("flush to ffmpeg: %w", err)
	}

	e.frameNum++

	// Read available H.264 data - use longer timeout for encoder warmup
	// First frames need much longer due to encoder initialization
	timeout := 100 * time.Millisecond
	if e.frameNum < 10 {
		timeout = 500 * time.Millisecond // Much longer timeout for initial frames
	}

	select {
	case data, ok := <-e.outputBuf:
		if !ok {
			return nil, fmt.Errorf("encoder output closed")
		}
		return data, nil
	case <-time.After(timeout):
		// No data available yet - encoder is still processing
		// Return nil without error to allow frame loop to continue
		return nil, nil
	}
}

// EncodeRaw sends raw RGBA bytes to ffmpeg
func (e *H264Encoder) EncodeRaw(data []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil, fmt.Errorf("encoder is closed")
	}

	// Set busy flag
	atomic.StoreInt32(&e.busy, 1)
	defer atomic.StoreInt32(&e.busy, 0)

	// Send raw RGBA pixels into ffmpeg using buffered writer
	_, err := e.stdinBuf.Write(data)
	if err != nil {
		return nil, fmt.Errorf("write to ffmpeg: %w", err)
	}

	// Flush the buffer to ensure data is sent
	if err := e.stdinBuf.Flush(); err != nil {
		return nil, fmt.Errorf("flush to ffmpeg: %w", err)
	}

	e.frameNum++

	// Read available H.264 data
	timeout := 100 * time.Millisecond
	if e.frameNum < 10 {
		timeout = 500 * time.Millisecond
	}

	select {
	case out, ok := <-e.outputBuf:
		if !ok {
			return nil, fmt.Errorf("encoder output closed")
		}
		return out, nil
	case <-time.After(timeout):
		return nil, nil
	}
}

// EncodeWithNALs sends a frame and returns parsed NAL units
func (e *H264Encoder) EncodeWithNALs(img image.Image) ([][]byte, error) {
	data, err := e.Encode(img)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return ParseNALUnits(data), nil
}

// EncodeRawWithNALs sends raw bytes and returns parsed NAL units
func (e *H264Encoder) EncodeRawWithNALs(data []byte) ([][]byte, error) {
	encoded, err := e.EncodeRaw(data)
	if err != nil {
		return nil, err
	}
	if encoded == nil {
		return nil, nil
	}
	return ParseNALUnits(encoded), nil
}

// ParseNALUnits splits H.264 Annex B stream into individual NAL units
func ParseNALUnits(data []byte) [][]byte {
	var nals [][]byte

	// Find all start codes (0x00 0x00 0x01 or 0x00 0x00 0x00 0x01)
	startCode3 := []byte{0x00, 0x00, 0x01}
	startCode4 := []byte{0x00, 0x00, 0x00, 0x01}

	var positions []int

	for i := 0; i < len(data); {
		if i+4 <= len(data) && bytes.Equal(data[i:i+4], startCode4) {
			positions = append(positions, i)
			i += 4
		} else if i+3 <= len(data) && bytes.Equal(data[i:i+3], startCode3) {
			positions = append(positions, i)
			i += 3
		} else {
			i++
		}
	}

	// Extract NAL units between start codes
	for i := 0; i < len(positions); i++ {
		start := positions[i]
		var end int
		if i+1 < len(positions) {
			end = positions[i+1]
		} else {
			end = len(data)
		}

		// Skip the start code itself
		nalStart := start + 3
		if start+4 <= len(data) && bytes.Equal(data[start:start+4], startCode4) {
			nalStart = start + 4
		}

		if nalStart < end {
			// Include start code for WebRTC (Annex B format)
			nals = append(nals, data[start:end])
		}
	}

	return nals
}

func (e *H264Encoder) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.stdin.Close()
	return e.cmd.Wait()
}

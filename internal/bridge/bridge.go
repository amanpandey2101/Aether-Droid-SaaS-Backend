// Package bridge provides GPU-accelerated video streaming from emulator containers.
// This replaces the old RGBA → gRPC → Go → FFmpeg pipeline with a more efficient
// virtio-gpu → ffmpeg HW encoder → H264 NAL → WebRTC bridge architecture.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// Bridge represents a video streaming bridge between an emulator container and WebRTC
type Bridge struct {
	videoTrack *webrtc.TrackLocalStaticSample
	cmd        *exec.Cmd
	fps        int
	width      int
	height     int
	running    bool
	mu         sync.Mutex
	stopCh     chan struct{}
	sps        []byte
	pps        []byte
}

// BridgeConfig contains configuration for the bridge
type BridgeConfig struct {
	FPS           int
	Width         int
	Height        int
	UseVAAPI      bool // Use Video Acceleration API (Intel/AMD)
	UseNVENC      bool // Use NVIDIA hardware encoding
	RenderDevice  string // Path to render device (e.g., /dev/dri/renderD128)
}

// DefaultConfig returns a default configuration for WSL2 VAAPI
func DefaultConfig() *BridgeConfig {
	return &BridgeConfig{
		FPS:          30,
		Width:        1280,
		Height:       720,
		UseVAAPI:     true,
		UseNVENC:     false,
		RenderDevice: "/dev/dri/renderD128",
	}
}

// NewBridge creates a new video streaming bridge
func NewBridge(videoTrack *webrtc.TrackLocalStaticSample, cfg *BridgeConfig) *Bridge {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &Bridge{
		videoTrack: videoTrack,
		fps:        cfg.FPS,
		width:      cfg.Width,
		height:     cfg.Height,
		stopCh:     make(chan struct{}),
	}
}

// StartBridge starts the bridge with VAAPI hardware encoding
// This runs ffmpeg inside the container to capture from virtio-gpu and encode to H.264
func StartBridge(videoTrack *webrtc.TrackLocalStaticSample, fps int) error {
	cfg := &BridgeConfig{
		FPS:          fps,
		Width:        1280,
		Height:       720,
		UseVAAPI:     true,
		RenderDevice: "/dev/dri/renderD128",
	}
	
	bridge := NewBridge(videoTrack, cfg)
	return bridge.Start()
}

// Start begins the video capture and encoding pipeline
func (b *Bridge) Start() error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("bridge already running")
	}
	b.running = true
	b.mu.Unlock()

	log.Printf("🎬 Starting GPU bridge: %dx%d @ %d fps", b.width, b.height, b.fps)

	// Try VAAPI first, then NVENC, then software fallback
	var err error
	if b.cmd, err = b.createVAAPICommand(); err != nil {
		log.Printf("⚠️  VAAPI not available, trying NVENC: %v", err)
		if b.cmd, err = b.createNVENCCommand(); err != nil {
			log.Printf("⚠️  NVENC not available, falling back to software: %v", err)
			if b.cmd, err = b.createSoftwareCommand(); err != nil {
				return fmt.Errorf("failed to create any encoder: %w", err)
			}
		}
	}

	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := b.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// Log stderr for debugging
	go b.logStderr(stderr)

	if err := b.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}

	// Start reading H.264 NAL units and sending to WebRTC
	go b.readAndSendNALs(stdout)

	log.Printf("✅ GPU bridge started successfully")
	return nil
}

// createVAAPICommand creates ffmpeg command for VAAPI hardware encoding (Intel/AMD)
func (b *Bridge) createVAAPICommand() (*exec.Cmd, error) {
	// Build ffmpeg command for VAAPI hardware encoding
	// This captures from DRM/KMS device and encodes using GPU
	args := []string{
		"-loglevel", "warning",
		// Hardware acceleration
		"-hwaccel", "vaapi",
		"-hwaccel_device", "/dev/dri/renderD128",
		"-hwaccel_output_format", "vaapi",
		// Input: capture from KMS/DRM framebuffer
		"-f", "kmsgrab",
		"-framerate", fmt.Sprintf("%d", b.fps),
		"-i", "-",
		// Video filter: scale using VAAPI
		"-vf", fmt.Sprintf("hwmap,scale_vaapi=w=%d:h=%d:format=nv12", b.width, b.height),
		// H.264 VAAPI encoder
		"-c:v", "h264_vaapi",
		"-profile:v", "high",
		"-level", "4.1",
		"-g", fmt.Sprintf("%d", b.fps),
		"-keyint_min", fmt.Sprintf("%d", b.fps),
		"-bf", "0", // No B-frames for low latency
		"-qp", "23",
		// Output: Annex-B format to stdout
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	}

	cmd := exec.Command("ffmpeg", args...)
	return cmd, nil
}

// createNVENCCommand creates ffmpeg command for NVIDIA hardware encoding
func (b *Bridge) createNVENCCommand() (*exec.Cmd, error) {
	args := []string{
		"-loglevel", "warning",
		// NVIDIA hardware acceleration
		"-hwaccel", "cuda",
		"-hwaccel_output_format", "cuda",
		// Input: capture from X11 or framebuffer
		"-f", "x11grab",
		"-framerate", fmt.Sprintf("%d", b.fps),
		"-video_size", fmt.Sprintf("%dx%d", b.width, b.height),
		"-i", ":0",
		// Convert to CUDA format for NVENC
		"-vf", "format=yuv420p,hwupload_cuda",
		// NVENC encoder
		"-c:v", "h264_nvenc",
		"-preset", "p1", // ultra-fast
		"-tune", "ull",  // ultra-low latency
		"-rc", "constqp",
		"-qp", "22",
		"-g", fmt.Sprintf("%d", b.fps),
		"-keyint_min", "1",
		"-profile:v", "high",
		"-level", "5.1",
		// Output
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	}

	cmd := exec.Command("ffmpeg", args...)
	return cmd, nil
}

// createSoftwareCommand creates ffmpeg command for software encoding (fallback)
func (b *Bridge) createSoftwareCommand() (*exec.Cmd, error) {
	args := []string{
		"-loglevel", "warning",
		// Input: virtual framebuffer or test source
		"-f", "x11grab",
		"-framerate", fmt.Sprintf("%d", b.fps),
		"-video_size", fmt.Sprintf("%dx%d", b.width, b.height),
		"-i", ":0",
		// Software x264 encoding
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", b.fps),
		"-keyint_min", "1",
		"-profile:v", "baseline",
		"-level", "4.2",
		"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:no-scenecut", b.fps, b.fps),
		// Output
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"-flags", "+cgop",
		"-flush_packets", "1",
		"pipe:1",
	}

	cmd := exec.Command("ffmpeg", args...)
	return cmd, nil
}

// logStderr logs ffmpeg stderr output
func (b *Bridge) logStderr(stderr io.ReadCloser) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			log.Printf("📹 FFmpeg: %s", line)
		}
	}
}

// readAndSendNALs reads H.264 NAL units from stdout and sends to WebRTC
func (b *Bridge) readAndSendNALs(stdout io.ReadCloser) {
	defer stdout.Close()
	defer b.Stop()

	buf := make([]byte, 256*1024) // 256KB buffer
	nalBuffer := make([]byte, 0, 1024*1024) // 1MB NAL buffer
	frameDuration := time.Second / time.Duration(b.fps)

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		n, err := stdout.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("❌ Bridge read error: %v", err)
			}
			return
		}

		if n == 0 {
			continue
		}

		// Append to NAL buffer
		nalBuffer = append(nalBuffer, buf[:n]...)

		// Parse and send NAL units
		nals := b.parseNALUnits(nalBuffer)
		if len(nals) > 0 {
			// Keep incomplete NAL at the end
			lastNALEnd := 0
			for _, nal := range nals {
				if err := b.sendNAL(nal, frameDuration); err != nil {
					log.Printf("❌ Failed to send NAL: %v", err)
				}
				lastNALEnd += len(nal)
			}
			// Shift remaining data to beginning
			nalBuffer = nalBuffer[lastNALEnd:]
		}

		// Prevent buffer from growing too large
		if len(nalBuffer) > 2*1024*1024 {
			log.Printf("⚠️  NAL buffer overflow, resetting")
			nalBuffer = nalBuffer[:0]
		}
	}
}

// parseNALUnits splits H.264 Annex-B stream into individual NAL units
func (b *Bridge) parseNALUnits(data []byte) [][]byte {
	var nals [][]byte

	startCode3 := []byte{0x00, 0x00, 0x01}
	startCode4 := []byte{0x00, 0x00, 0x00, 0x01}

	var positions []int

	for i := 0; i < len(data)-4; {
		if bytes.Equal(data[i:i+4], startCode4) {
			positions = append(positions, i)
			i += 4
		} else if bytes.Equal(data[i:i+3], startCode3) {
			positions = append(positions, i)
			i += 3
		} else {
			i++
		}
	}

	// Extract complete NAL units (between start codes)
	for i := 0; i < len(positions)-1; i++ {
		start := positions[i]
		end := positions[i+1]
		if end > start {
			nals = append(nals, data[start:end])
		}
	}

	return nals
}

// sendNAL sends a single NAL unit to WebRTC
func (b *Bridge) sendNAL(nal []byte, duration time.Duration) error {
	if len(nal) < 5 {
		return nil
	}

	// Get NAL type
	nalType := b.getNALType(nal)

	switch nalType {
	case 7: // SPS
		b.sps = append([]byte(nil), nal...)
		log.Printf("📦 Stored SPS (%d bytes)", len(nal))
	case 8: // PPS
		b.pps = append([]byte(nil), nal...)
		log.Printf("📦 Stored PPS (%d bytes)", len(nal))
	case 5: // IDR frame - prepend SPS/PPS
		if b.sps != nil && b.pps != nil {
			// Send SPS
			if err := b.videoTrack.WriteSample(media.Sample{
				Data:     b.sps,
				Duration: duration,
			}); err != nil {
				return err
			}
			// Send PPS
			if err := b.videoTrack.WriteSample(media.Sample{
				Data:     b.pps,
				Duration: duration,
			}); err != nil {
				return err
			}
		}
	}

	// Send the NAL unit
	return b.videoTrack.WriteSample(media.Sample{
		Data:     nal,
		Duration: duration,
	})
}

// getNALType extracts the NAL type from a NAL unit with start code
func (b *Bridge) getNALType(nal []byte) int {
	if len(nal) < 5 {
		return -1
	}

	// Skip start code
	i := 0
	if nal[0] == 0x00 && nal[1] == 0x00 && nal[2] == 0x00 && nal[3] == 0x01 {
		i = 4
	} else if nal[0] == 0x00 && nal[1] == 0x00 && nal[2] == 0x01 {
		i = 3
	}

	if i >= len(nal) {
		return -1
	}

	return int(nal[i] & 0x1F)
}

// Stop stops the bridge
func (b *Bridge) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.running {
		return nil
	}

	close(b.stopCh)
	b.running = false

	if b.cmd != nil && b.cmd.Process != nil {
		if err := b.cmd.Process.Kill(); err != nil {
			log.Printf("⚠️  Failed to kill ffmpeg process: %v", err)
		}
		b.cmd.Wait()
	}

	log.Printf("🛑 GPU bridge stopped")
	return nil
}

// IsRunning returns whether the bridge is currently running
func (b *Bridge) IsRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// CreateContainerBridgeExecArgs returns the command arguments to start the bridge inside a container
func CreateContainerBridgeExecArgs(fps int) []string {
	return []string{
		"/usr/local/bin/bridge",
		fmt.Sprintf("--fps=%d", fps),
	}
}

// DockerExecBridge is used by the container manager to start the bridge via Docker exec
// This function is called from the Go server to execute the bridge process inside the container
type DockerExecBridge struct {
	ContainerID string
	FPS         int
	Context     context.Context
}

// GetFFmpegVAAPIArgs returns ffmpeg arguments for VAAPI encoding inside container
func GetFFmpegVAAPIArgs(width, height, fps int) []string {
	return []string{
		"ffmpeg",
		"-loglevel", "warning",
		"-hwaccel", "vaapi",
		"-hwaccel_device", "/dev/dri/renderD128",
		"-f", "kmsgrab",
		"-i", "-",
		"-vf", fmt.Sprintf("hwmap,scale_vaapi=w=%d:h=%d", width, height),
		"-c:v", "h264_vaapi",
		"-profile:v", "high",
		"-level", "4.1",
		"-g", fmt.Sprintf("%d", fps),
		"-keyint_min", fmt.Sprintf("%d", fps),
		"-f", "h264",
		"pipe:1",
	}
}

// GetFFmpegNVENCArgs returns ffmpeg arguments for NVENC encoding inside container
func GetFFmpegNVENCArgs(width, height, fps int) []string {
	return []string{
		"ffmpeg",
		"-loglevel", "warning",
		"-f", "x11grab",
		"-framerate", fmt.Sprintf("%d", fps),
		"-video_size", fmt.Sprintf("%dx%d", width, height),
		"-i", ":0",
		"-vf", "format=yuv420p,hwupload_cuda",
		"-c:v", "h264_nvenc",
		"-preset", "p1",
		"-tune", "ull",
		"-rc", "constqp",
		"-qp", "22",
		"-g", fmt.Sprintf("%d", fps),
		"-keyint_min", "1",
		"-profile:v", "high",
		"-level", "5.1",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	}
}

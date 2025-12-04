// Package bridge provides container-level bridge management for GPU streaming.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// ContainerBridge manages a GPU streaming bridge for a specific container
type ContainerBridge struct {
	ContainerID string
	videoTrack  *webrtc.TrackLocalStaticSample
	dockerHost  string
	httpClient  *http.Client
	execID      string
	running     bool
	mu          sync.Mutex
	stopCh      chan struct{}
	fps         int
	width       int
	height      int
	sps         []byte
	pps         []byte
}

// ContainerBridgeConfig configuration for container bridge
type ContainerBridgeConfig struct {
	ContainerID string
	DockerHost  string
	HTTPClient  *http.Client
	FPS         int
	Width       int
	Height      int
}

// NewContainerBridge creates a new bridge for a Docker container
func NewContainerBridge(videoTrack *webrtc.TrackLocalStaticSample, cfg *ContainerBridgeConfig) *ContainerBridge {
	if cfg.FPS == 0 {
		cfg.FPS = 30
	}
	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 720
	}

	return &ContainerBridge{
		ContainerID: cfg.ContainerID,
		videoTrack:  videoTrack,
		dockerHost:  cfg.DockerHost,
		httpClient:  cfg.HTTPClient,
		fps:         cfg.FPS,
		width:       cfg.Width,
		height:      cfg.Height,
		stopCh:      make(chan struct{}),
	}
}

// Docker API structures for exec
type dockerExecCreateRequest struct {
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
}

type dockerExecCreateResponse struct {
	ID string `json:"Id"`
}

type dockerExecStartRequest struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

// Start starts the GPU bridge inside the container using Docker exec
func (cb *ContainerBridge) Start(ctx context.Context) error {
	cb.mu.Lock()
	if cb.running {
		cb.mu.Unlock()
		return fmt.Errorf("bridge already running for container %s", cb.ContainerID)
	}
	cb.running = true
	cb.mu.Unlock()

	log.Printf("🚀 Starting container bridge for %s", cb.ContainerID[:12])

	// Check if we have GPU devices available by trying to detect /dev/dri
	hasGPU := cb.checkGPUAvailability(ctx)

	var err error
	if hasGPU {
		// Try hardware encoders first
		log.Printf("🎮 GPU detected, trying hardware encoding...")
		err = cb.startWithEncoder(ctx, "vaapi")
		if err != nil {
			log.Printf("⚠️ VAAPI failed, trying NVENC: %v", err)
			err = cb.startWithEncoder(ctx, "nvenc")
		}
	}

	if !hasGPU || err != nil {
		// Fallback to software encoding (works on all instances including cloud VMs)
		log.Printf("💻 Using software encoding (no GPU or hardware encoding failed)")
		err = cb.startWithEncoder(ctx, "software")
		if err != nil {
			cb.mu.Lock()
			cb.running = false
			cb.mu.Unlock()
			return fmt.Errorf("software encoder failed: %w", err)
		}
	}

	log.Printf("✅ Container bridge started for %s", cb.ContainerID[:12])
	return nil
}

// checkGPUAvailability checks if GPU devices are available in the container
func (cb *ContainerBridge) checkGPUAvailability(ctx context.Context) bool {
	// Create exec to check for /dev/dri
	checkCmd := []string{"sh", "-c", "test -e /dev/dri/renderD128 && echo 'GPU_AVAILABLE'"}

	execReq := dockerExecCreateRequest{
		AttachStdout: true,
		AttachStderr: false,
		Tty:          false,
		Cmd:          checkCmd,
	}

	reqBody, err := json.Marshal(execReq)
	if err != nil {
		log.Printf("⚠️ Failed to check GPU: %v", err)
		return false
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("%s/v1.44/containers/%s/exec", cb.dockerHost, cb.ContainerID),
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cb.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return false
	}

	var execResp dockerExecCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return false
	}

	// Start exec and check output
	startReq := dockerExecStartRequest{Detach: false, Tty: false}
	startBody, _ := json.Marshal(startReq)

	startHttpReq, _ := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("%s/v1.44/exec/%s/start", cb.dockerHost, execResp.ID),
		bytes.NewReader(startBody),
	)
	startHttpReq.Header.Set("Content-Type", "application/json")

	startResp, err := cb.httpClient.Do(startHttpReq)
	if err != nil {
		return false
	}
	defer startResp.Body.Close()

	output, _ := io.ReadAll(startResp.Body)
	hasGPU := strings.Contains(string(output), "GPU_AVAILABLE")

	if hasGPU {
		log.Printf("✅ GPU device detected in container")
	} else {
		log.Printf("ℹ️ No GPU device found - will use software encoding")
	}

	return hasGPU
}

// startWithEncoder attempts to start the bridge with a specific encoder type
func (cb *ContainerBridge) startWithEncoder(ctx context.Context, encoderType string) error {
	// Build ffmpeg command based on encoder type
	var cmd []string
	switch encoderType {
	case "vaapi":
		cmd = cb.buildVAAPICommand()
	case "nvenc":
		cmd = cb.buildNVENCCommand()
	default:
		cmd = cb.buildSoftwareCommand()
	}

	// Create exec instance
	execReq := dockerExecCreateRequest{
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          cmd,
	}

	reqBody, err := json.Marshal(execReq)
	if err != nil {
		return fmt.Errorf("failed to marshal exec request: %w", err)
	}

	// Create exec
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("%s/v1.44/containers/%s/exec", cb.dockerHost, cb.ContainerID),
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return fmt.Errorf("failed to create exec request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cb.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create exec: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("exec create failed: %s - %s", resp.Status, string(body))
	}

	var execResp dockerExecCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return fmt.Errorf("failed to decode exec response: %w", err)
	}

	cb.execID = execResp.ID
	log.Printf("📦 Created exec %s for container %s with %s", cb.execID[:12], cb.ContainerID[:12], encoderType)

	// Start exec and process output
	go cb.runExec(ctx, encoderType)

	return nil
}

// buildVAAPICommand builds ffmpeg command for VAAPI encoding
func (cb *ContainerBridge) buildVAAPICommand() []string {
	return []string{
		"ffmpeg",
		"-loglevel", "warning",
		"-hwaccel", "vaapi",
		"-hwaccel_device", "/dev/dri/renderD128",
		"-hwaccel_output_format", "vaapi",
		"-f", "kmsgrab",
		"-framerate", fmt.Sprintf("%d", cb.fps),
		"-i", "-",
		"-vf", fmt.Sprintf("hwmap,scale_vaapi=w=%d:h=%d:format=nv12", cb.width, cb.height),
		"-c:v", "h264_vaapi",
		"-profile:v", "high",
		"-level", "4.1",
		"-g", fmt.Sprintf("%d", cb.fps),
		"-keyint_min", fmt.Sprintf("%d", cb.fps),
		"-bf", "0",
		"-qp", "23",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	}
}

// buildNVENCCommand builds ffmpeg command for NVENC encoding
func (cb *ContainerBridge) buildNVENCCommand() []string {
	return []string{
		"ffmpeg",
		"-loglevel", "warning",
		"-hwaccel", "cuda",
		"-hwaccel_output_format", "cuda",
		"-f", "x11grab",
		"-framerate", fmt.Sprintf("%d", cb.fps),
		"-video_size", fmt.Sprintf("%dx%d", cb.width, cb.height),
		"-i", ":0",
		"-vf", "format=yuv420p,hwupload_cuda",
		"-c:v", "h264_nvenc",
		"-preset", "p1",
		"-tune", "ull",
		"-rc", "constqp",
		"-qp", "22",
		"-g", fmt.Sprintf("%d", cb.fps),
		"-keyint_min", "1",
		"-profile:v", "high",
		"-level", "5.1",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"pipe:1",
	}
}

// buildSoftwareCommand builds optimized ffmpeg command for software encoding on cloud VMs
// This is designed for GCP/AWS instances without GPU
func (cb *ContainerBridge) buildSoftwareCommand() []string {
	// Use a shell script to:
	// 1. Ensure DISPLAY is set
	// 2. Use optimal x264 settings for cloud CPUs (many cores, no GPU)
	// 3. Lower resolution and framerate for smooth streaming

	// Optimize for cloud: 720p @ 20fps with fast x264
	targetFPS := 20 // Lower FPS for CPU encoding
	if cb.fps < targetFPS {
		targetFPS = cb.fps
	}

	// Use constrained baseline for maximum compatibility and fast decode
	return []string{
		"sh", "-c",
		fmt.Sprintf(`
			export DISPLAY=:0
			# Wait for X server to be ready
			for i in 1 2 3 4 5; do
				if xdpyinfo >/dev/null 2>&1; then
					break
				fi
				sleep 1
			done
			
			# Start ffmpeg with optimized settings for cloud CPU encoding
			exec ffmpeg -loglevel warning \
				-f x11grab \
				-framerate %d \
				-video_size %dx%d \
				-i :0 \
				-c:v libx264 \
				-preset superfast \
				-tune zerolatency \
				-crf 28 \
				-maxrate 1500k \
				-bufsize 3000k \
				-pix_fmt yuv420p \
				-g %d \
				-keyint_min 1 \
				-profile:v baseline \
				-level 3.1 \
				-threads 4 \
				-f h264 \
				-bsf:v h264_mp4toannexb \
				-fflags nobuffer \
				-flags low_delay \
				-flush_packets 1 \
				pipe:1
		`, targetFPS, cb.width, cb.height, targetFPS),
	}
}

// runExec starts the exec and processes the output
func (cb *ContainerBridge) runExec(ctx context.Context, encoderType string) {
	defer func() {
		cb.mu.Lock()
		cb.running = false
		cb.mu.Unlock()
	}()

	// Start exec
	startReq := dockerExecStartRequest{
		Detach: false,
		Tty:    false,
	}

	reqBody, err := json.Marshal(startReq)
	if err != nil {
		log.Printf("❌ Failed to marshal start request: %v", err)
		return
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		fmt.Sprintf("%s/v1.44/exec/%s/start", cb.dockerHost, cb.execID),
		bytes.NewReader(reqBody),
	)
	if err != nil {
		log.Printf("❌ Failed to create start request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cb.httpClient.Do(req)
	if err != nil {
		log.Printf("❌ Failed to start exec: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("❌ Exec start failed: %s - %s", resp.Status, string(body))
		return
	}

	log.Printf("🎬 Bridge exec started with %s encoder", encoderType)

	// Read and process H.264 output
	cb.processH264Stream(resp.Body)
}

// processH264Stream reads H.264 data and sends NALs to WebRTC
func (cb *ContainerBridge) processH264Stream(reader io.Reader) {
	buf := make([]byte, 64*1024)
	nalBuffer := make([]byte, 0, 512*1024)
	frameDuration := time.Second / time.Duration(cb.fps)
	frameCount := 0

	for {
		select {
		case <-cb.stopCh:
			return
		default:
		}

		n, err := reader.Read(buf)
		if err != nil {
			if err != io.EOF {
				log.Printf("❌ Stream read error: %v", err)
			}
			return
		}

		if n == 0 {
			continue
		}

		nalBuffer = append(nalBuffer, buf[:n]...)

		// Parse NAL units
		for {
			nal, remaining := cb.extractNextNAL(nalBuffer)
			if nal == nil {
				break
			}
			nalBuffer = remaining

			if err := cb.sendNAL(nal, frameDuration); err != nil {
				log.Printf("❌ Failed to send NAL: %v", err)
			}

			frameCount++
			if frameCount%150 == 0 { // Log every ~5 seconds at 30fps
				log.Printf("📡 Sent %d frames via GPU bridge", frameCount)
			}
		}

		// Prevent buffer overflow
		if len(nalBuffer) > 1024*1024 {
			log.Printf("⚠️ NAL buffer overflow, resetting")
			nalBuffer = nalBuffer[:0]
		}
	}
}

// extractNextNAL extracts the next complete NAL unit from buffer
func (cb *ContainerBridge) extractNextNAL(data []byte) ([]byte, []byte) {
	if len(data) < 4 {
		return nil, data
	}

	// Find first start code
	start := -1
	for i := 0; i < len(data)-3; i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 {
			if data[i+2] == 0x01 {
				start = i
				break
			}
			if i < len(data)-4 && data[i+2] == 0x00 && data[i+3] == 0x01 {
				start = i
				break
			}
		}
	}

	if start < 0 {
		return nil, data
	}

	// Find next start code
	for i := start + 3; i < len(data)-3; i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 {
			if data[i+2] == 0x01 {
				return data[start:i], data[i:]
			}
			if i < len(data)-4 && data[i+2] == 0x00 && data[i+3] == 0x01 {
				return data[start:i], data[i:]
			}
		}
	}

	// No complete NAL yet
	return nil, data
}

// sendNAL sends a NAL unit to WebRTC
func (cb *ContainerBridge) sendNAL(nal []byte, duration time.Duration) error {
	if len(nal) < 5 {
		return nil
	}

	nalType := cb.getNALType(nal)

	switch nalType {
	case 7: // SPS
		cb.sps = append([]byte(nil), nal...)
		log.Printf("📦 Stored SPS (%d bytes)", len(nal))
	case 8: // PPS
		cb.pps = append([]byte(nil), nal...)
		log.Printf("📦 Stored PPS (%d bytes)", len(nal))
	case 5: // IDR - send SPS/PPS first
		if cb.sps != nil && cb.pps != nil {
			if err := cb.videoTrack.WriteSample(media.Sample{Data: cb.sps, Duration: duration}); err != nil {
				return err
			}
			if err := cb.videoTrack.WriteSample(media.Sample{Data: cb.pps, Duration: duration}); err != nil {
				return err
			}
		}
	}

	return cb.videoTrack.WriteSample(media.Sample{
		Data:     nal,
		Duration: duration,
	})
}

// getNALType extracts NAL type from data with start code
func (cb *ContainerBridge) getNALType(nal []byte) int {
	if len(nal) < 5 {
		return -1
	}

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
func (cb *ContainerBridge) Stop() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if !cb.running {
		return nil
	}

	close(cb.stopCh)
	cb.running = false

	// Kill the exec process if running
	if cb.execID != "" {
		// Note: Docker doesn't have a direct way to kill an exec,
		// but it will terminate when the stream is closed
	}

	log.Printf("🛑 Container bridge stopped for %s", strings.TrimPrefix(cb.ContainerID, "")[:12])
	return nil
}

// IsRunning returns whether the bridge is running
func (cb *ContainerBridge) IsRunning() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.running
}

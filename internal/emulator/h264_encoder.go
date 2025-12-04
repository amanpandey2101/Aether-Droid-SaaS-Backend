package emulator

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

type H264Encoder struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	width     int
	height    int
	mu        sync.Mutex
	outputBuf chan []byte
	closed    bool
	frameNum  int64
}

func (e *H264Encoder) Output() <-chan []byte {
	return e.outputBuf
}

// NewH264Encoder creates a new H264 encoder optimized for ultra-low latency streaming
func NewH264Encoder(width, height int, fps int) (*H264Encoder, error) {
    // Try NVENC first
    enc, err := newX264Encoder(width, height, fps)
    if err == nil {
        log.Println("🔥 Using NVIDIA NVENC encoder")
        return enc, nil
    }

    log.Println("⚠️ NVENC not available, falling back to libx264:", err)

    // Fallback: software x264 encoder (always works, no GPU required)
    return newX264Encoder(width, height, fps)
}


// newNVENCEncoder creates an NVIDIA hardware-accelerated encoder
func newNVENCEncoder(width, height int, fps int) (*H264Encoder, error) {
	// Simplified NVIDIA NVENC settings
	cmd := exec.Command("ffmpeg",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
		"-an",
		"-c:v", "h264_nvenc",
		"-preset", "fast",
		"-rc", "cbr",
		"-b:v", "2M",
		"-maxrate", "2M",
		"-bufsize", "1M",
		"-pix_fmt", "yuv420p",
		"-g", strconv.Itoa(fps*2), // Keyframe every 2 seconds
		"-keyint_min", "1",
		"-profile:v", "main",
		"-level", "4.0",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"-flags", "+cgop", // Ensure SPS/PPS are repeated
		"pipe:1",
	)

	return setupEncoder(cmd, width, height)
}

// newX264Encoder creates a software x264 encoder (fallback)
func newX264Encoder(width, height int, fps int) (*H264Encoder, error) {
	cmd := exec.Command("ffmpeg",
		// Input raw video
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
		"-vf", "scale=540:-1",  

		// No audio
		"-an",


		"-c:v", "libx264",
		"-preset", "veryfast",        
		"-tune", "zerolatency",      

		// 🔥 Bitrate & rate control
		"-b:v", "2M",                  // Target bitrate
		"-maxrate", "2M",
		"-bufsize", "1M",

		// Pixel format for WebRTC
		"-pix_fmt", "yuv420p",

		// Keyframe interval
		"-g", fmt.Sprintf("%d", fps*2), // Keyframe every 2 seconds
		"-keyint_min", "1",

		// Profile for compatibility
		"-profile:v", "baseline",
		"-level", "4.1",

		// ⚡ Critical x264 parameters for latency
		"-x264-params",
		// No B-frames, instant decode, repeated SPS/PPS, no scenecut
		"bframes=0:repeat-headers=1:scenecut=0:keyint=30:min-keyint=1",

		// Output format
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb",
		"-flush_packets", "1",

		// Output pipe
		"pipe:1",
	)

	return setupEncoder(cmd, width, height)
}


// setupEncoder initializes the encoder process
func setupEncoder(cmd *exec.Cmd, width, height int) (*H264Encoder, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

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
				log.Printf("🎬 FFmpeg: %s", string(buf[:n]))
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	enc := &H264Encoder{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		width:     width,
		height:    height,
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
			log.Printf("🔴 H264 encoder read loop ended: %v (total bytes read: %d)", err, totalBytes)
			close(e.outputBuf)
			return
		}
		if n > 0 {
			totalBytes += n
			log.Printf("📦 H264 encoder produced %d bytes (total: %d)", n, totalBytes)

			// Copy the data since we're reusing the buffer
			data := make([]byte, n)
			copy(data, readBuf[:n])

			// Non-blocking send - drop old frames if buffer is full to keep latency low
			select {
			case e.outputBuf <- data:
				log.Printf("✅ H264 data queued for sending")
			default:
				// Buffer full - drain oldest and add new
				log.Printf("⚠️ H264 buffer full, dropping old frame")
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

	// Convert image.Image → RGBA byte slice
	rgba, ok := img.(*image.RGBA)
	if !ok {
		return nil, fmt.Errorf("image is not RGBA")
	}

	// Send raw RGBA pixels into ffmpeg
	_, err := e.stdin.Write(rgba.Pix)
	if err != nil {
		return nil, fmt.Errorf("write to ffmpeg: %w", err)
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

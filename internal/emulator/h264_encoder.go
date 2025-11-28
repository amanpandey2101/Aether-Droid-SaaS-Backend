package emulator

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"log"
	"os/exec"
	"sync"
)

type H264Encoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	width  int
	height int
	mu     sync.Mutex
	buf    []byte
}

func NewH264Encoder(width, height int, fps int) (*H264Encoder, error) {
	cmd := exec.Command("ffmpeg",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", fmt.Sprintf("%d", fps),
		"-i", "pipe:0",
		"-an",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-profile:v", "baseline", // Better browser compatibility
		"-level", "3.1",
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", fps), // Keyframe every second
		"-keyint_min", fmt.Sprintf("%d", fps),
		"-sc_threshold", "0",
		"-b:v", "2M",
		"-maxrate", "2M",
		"-bufsize", "4M",
		"-f", "h264",
		"-bsf:v", "h264_mp4toannexb", // Ensure Annex B format with start codes
		"pipe:1",
	)

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
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				log.Printf("[ffmpeg] %s", string(buf[:n]))
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	log.Printf("✅ H264 encoder started: %dx%d @ %d fps", width, height, fps)

	return &H264Encoder{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		width:  width,
		height: height,
		buf:    make([]byte, 0, 1024*1024), // 1MB buffer
	}, nil
}

// Encode sends an RGBA frame to ffmpeg and reads back H.264 NAL units
func (e *H264Encoder) Encode(img image.Image) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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

	// Read available H.264 data from ffmpeg
	// ffmpeg buffers internally, so we read what's available
	readBuf := make([]byte, 256*1024) // 256KB read buffer
	n, err := e.stdout.Read(readBuf)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read from ffmpeg: %w", err)
	}

	if n == 0 {
		return nil, nil
	}

	return readBuf[:n], nil
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
	e.stdin.Close()
	return e.cmd.Wait()
}

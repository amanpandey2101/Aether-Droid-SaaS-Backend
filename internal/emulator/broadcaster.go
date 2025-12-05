package emulator

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type Broadcaster struct {
	mu    sync.Mutex
	track *webrtc.TrackLocalStaticSample
	fps   int
}

func NewBroadcaster(track *webrtc.TrackLocalStaticSample) *Broadcaster {
	return &Broadcaster{
		track: track,
		fps:   30, // Default 30 FPS
	}
}

func (b *Broadcaster) SetFPS(fps int) {
	b.fps = fps
}

// SendH264NALs sends H264 NAL units to WebRTC track
// Each NAL is sent as a separate sample with proper timing
func (b *Broadcaster) SendH264NALs(nals [][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Calculate frame duration based on FPS
	frameDuration := time.Second / time.Duration(b.fps)

	// Send all NALs for this frame as a single sample
	// This ensures they arrive together and decode properly
	var frameData []byte
	for _, nal := range nals {
		if len(nal) > 0 {
			frameData = append(frameData, nal...)
		}
	}

	if len(frameData) == 0 {
		return nil
	}

	return b.track.WriteSample(media.Sample{
		Data:     frameData,
		Duration: frameDuration,
	})
}

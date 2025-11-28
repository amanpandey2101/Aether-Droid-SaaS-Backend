package emulator

import (
	"log"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// Broadcaster sends H.264 encoded frames over WebRTC
type Broadcaster struct {
	Track    *webrtc.TrackLocalStaticSample
	seqNum   uint16
	timestamp uint32
}

func NewBroadcaster(track *webrtc.TrackLocalStaticSample) *Broadcaster {
	log.Println("📡 Broadcaster created, track ID:", track.ID())
	return &Broadcaster{
		Track: track,
	}
}

// SendH264 sends H.264 NAL unit data as a WebRTC sample
func (b *Broadcaster) SendH264(frame []byte) error {
	if len(frame) == 0 {
		return nil
	}

	err := b.Track.WriteSample(media.Sample{
		Data:     frame,
		Duration: time.Second / 30,
	})
	
	if err != nil {
		log.Printf("❌ WriteSample error: %v", err)
		return err
	}

	return nil
}

// SendH264NALs sends multiple NAL units as separate samples
func (b *Broadcaster) SendH264NALs(nals [][]byte) error {
	for _, nal := range nals {
		if err := b.SendH264(nal); err != nil {
			return err
		}
	}
	return nil
}

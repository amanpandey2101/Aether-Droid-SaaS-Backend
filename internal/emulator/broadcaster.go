package emulator

import (
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type Broadcaster struct {
	mu    sync.Mutex
	track *webrtc.TrackLocalStaticSample

	sps     []byte
	pps     []byte
	spsSent bool
	ppsSent bool
}

func NewBroadcaster(track *webrtc.TrackLocalStaticSample) *Broadcaster {
	return &Broadcaster{track: track}
}


func (b *Broadcaster) writeNAL(nal []byte) error {
	if len(nal) == 0 {
		return nil
	}
	return b.track.WriteSample(media.Sample{
		Data:     nal,
		Duration: time.Second / 30, // 30 FPS
	})
}

// Safe NALU type parser (works for both 3-byte and 4-byte start codes)
func getNALType(nal []byte) int {
	if len(nal) < 5 {
		return -1 // invalid
	}

	// Find the first byte after the start code
	i := 0
	for i < len(nal)-4 {
		// 00 00 01
		if nal[i] == 0x00 && nal[i+1] == 0x00 && nal[i+2] == 0x01 {
			return int(nal[i+3] & 0x1F)
		}
		// 00 00 00 01
		if nal[i] == 0x00 && nal[i+1] == 0x00 && nal[i+2] == 0x00 && nal[i+3] == 0x01 {
			return int(nal[i+4] & 0x1F)
		}
		i++
	}

	return -1
}

func (b *Broadcaster) SendH264NALs(nals [][]byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}

		nalType := getNALType(nal)
		if nalType < 0 {
			log.Printf("⚠️ Skipping invalid NAL (len=%d)", len(nal))
			continue
		}

		switch nalType {
		case 7: // SPS
			b.sps = append([]byte(nil), nal...)
			log.Printf("📡 Stored and sending SPS (%d bytes)", len(nal))
			if err := b.writeNAL(nal); err != nil {
				return err
			}
			continue

		case 8: // PPS
			b.pps = append([]byte(nil), nal...)
			log.Printf("📡 Stored and sending PPS (%d bytes)", len(nal))
			if err := b.writeNAL(nal); err != nil {
				return err
			}
			continue
		}

		// For IDR frames (NAL type 5), always prepend SPS/PPS
		if nalType == 5 {
			if b.sps != nil && b.pps != nil {
				log.Printf("📡 Sending SPS/PPS before IDR")
				if err := b.writeNAL(b.sps); err != nil {
					return err
				}
				if err := b.writeNAL(b.pps); err != nil {
					return err
				}
			} else {
				log.Printf("⚠️ IDR frame but SPS/PPS not ready yet")
			}
		}

		// Send the actual frame NAL
		if err := b.writeNAL(nal); err != nil {
			log.Printf("❌ writeNAL error: %v", err)
			return err
		}
	}

	return nil
}

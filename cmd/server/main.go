package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"android_cloud_backend/internal/emulator"

	"github.com/pion/webrtc/v3"
)

var (
	broadcaster *emulator.Broadcaster
	emuClient   *emulator.EmulatorClient
	h264Enc     *emulator.H264Encoder
	mu          sync.Mutex
)

func main() {
	log.Println("Starting WebRTC Android Stream Server on :8080")

	// Get emulator address from environment or use default
	// Format: "host:port" (e.g., "34.132.33.72:8554" for gRPC emulator controller)
	// Note: Port 8554 is the standard gRPC emulator controller port
	// Port 5555 is typically used for ADB, not gRPC
	emulatorAddr := os.Getenv("EMULATOR_ADDR")
	if emulatorAddr == "" {
		emulatorAddr = "34.41.82.116:8554"
	}
	log.Printf("Connecting to emulator at: %s", emulatorAddr)

	// Initialize emulator client early
	var err error
	emuClient, err = emulator.NewEmulatorClient(emulatorAddr)
	if err != nil {
		log.Fatalf("Failed to connect to emulator: %v", err)
	}
	log.Printf("Connected to emulator gRPC at %s", emulatorAddr)

	// WebRTC endpoint
	http.HandleFunc("/offer", func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")


		if r.Method == http.MethodOptions {
			log.Println("⚠️  CORS Preflight request accepted")
			w.WriteHeader(http.StatusOK)
			return
		}

		// Handle the real POST request
		handleOffer(w, r)
	})

	// Start frame loop in background
	go startFrameLoop()

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleOffer(w http.ResponseWriter, r *http.Request) {
	log.Println("=== /offer called ===")

	// Read raw request for debugging
	raw, _ := io.ReadAll(r.Body)
	log.Println("Raw Offer JSON length:", len(raw))

	// Decode offer
	var offer webrtc.SessionDescription
	log.Println("r.ContentLength:", r.ContentLength)
	log.Println("r.Method:", r.Method)

	if err := json.Unmarshal(raw, &offer); err != nil {
		log.Println("JSON Unmarshal error:", err)
		http.Error(w, err.Error(), 400)
		return
	}

	log.Println("Offer Type:", offer.Type)
	log.Println("Offer SDP length:", len(offer.SDP))
	if len(offer.SDP) < 10 {
		log.Println("Offer SDP is empty or invalid")
		http.Error(w, "Invalid SDP", 400)
		return
	}

	// Create PeerConnection with H.264 codec preference
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		PayloadType: 96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		log.Println("RegisterCodec error:", err)
		http.Error(w, err.Error(), 500)
		return
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		log.Println("NewPeerConnection error:", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Println("✔ PeerConnection created")

	// Connection state logging
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Connection state: %s", state.String())
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE state: %s", state.String())
	})

	// Create H.264 video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"video", "android-screen",
	)
	if err != nil {
		log.Println("❌ NewTrackLocalStaticSample error:", err)
		http.Error(w, err.Error(), 500)
		return
	}

	_, err = pc.AddTrack(videoTrack)
	if err != nil {
		log.Println("❌ AddTrack error:", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Println("✔ H.264 video track added")

	// Set broadcaster
	mu.Lock()
	broadcaster = emulator.NewBroadcaster(videoTrack)
	mu.Unlock()

	// Apply remote offer
	if err := pc.SetRemoteDescription(offer); err != nil {
		log.Println("❌ SetRemoteDescription error:", err)
		http.Error(w, err.Error(), 500)
		return
	}
	log.Println("✔ Remote description set")

	// Create answer
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		log.Println("❌ CreateAnswer error:", err)
		http.Error(w, err.Error(), 500)
		return
	}

	// Apply local description
	if err := pc.SetLocalDescription(answer); err != nil {
		log.Println("❌ SetLocalDescription error:", err)
		http.Error(w, err.Error(), 500)
		return
	}

	log.Println("✔ Local description set")
	log.Println("Answer SDP length:", len(answer.SDP))

	// Respond with answer
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)

	log.Println("=== /offer completed ===")
}

func startFrameLoop() {
	const fps = 30
	frameDuration := time.Second / fps

	log.Println("🎬 Frame loop started, waiting for broadcaster...")

	for {
		mu.Lock()
		bc := broadcaster
		mu.Unlock()

		if bc == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		frameStart := time.Now()

		// Get frame from emulator
		img, err := emuClient.GetFrame()
		if err != nil {
			log.Println("❌ GetFrame error:", err)
			time.Sleep(frameDuration)
			continue
		}
		if img == nil {
			time.Sleep(frameDuration)
			continue
		}

		// Initialize encoder on first frame
		if h264Enc == nil {
			bounds := img.Bounds()
			w := bounds.Dx()
			h := bounds.Dy()
			h264Enc, err = emulator.NewH264Encoder(w, h, fps)
			if err != nil {
				log.Printf("❌ Failed to create H264 encoder: %v", err)
				time.Sleep(time.Second)
				continue
			}
			log.Printf("🎥 H.264 encoder initialized: %dx%d @ %d fps", w, h, fps)
		}

		// Encode frame to H.264
		h264Data, err := h264Enc.Encode(img)
		if err != nil {
			log.Println("❌ H264 Encode error:", err)
			continue
		}

		if h264Data != nil && len(h264Data) > 0 {
			if err := bc.SendH264(h264Data); err != nil {
				log.Println("❌ SendH264 error:", err)
			}
		}

		// Maintain frame rate
		elapsed := time.Since(frameStart)
		if elapsed < frameDuration {
			time.Sleep(frameDuration - elapsed)
		}
	}
}

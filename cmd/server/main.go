package main

import (
	"encoding/json"
	"image"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"

	"android_cloud_backend/internal/api"
	"android_cloud_backend/internal/config"
	"android_cloud_backend/internal/container"
	"android_cloud_backend/internal/database"
	"android_cloud_backend/internal/emulator"
	pb "android_cloud_backend/internal/emulator/proto"
)

var (
	broadcaster *emulator.Broadcaster
	emuClient   *emulator.EmulatorClient
	h264Enc     *emulator.H264Encoder
	mu          sync.Mutex
	cfg         *config.Config
)

func main() {
	// Load configuration
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found")
	}

	// Initialize database service
	dbService, err := database.NewService()
	if err != nil {
		log.Fatal("Database connection failed:", err)
	}
	cfg = config.Load()
	log.Println(" Starting Aether Android Cloud Backend")

	// Initialize container manager
	containerManager, err := container.NewManager(cfg, dbService)
	if err != nil {
		log.Printf(" Container manager not available: %v", err)
		log.Println(" Container management will return errors, but API routes are available")
		containerManager = nil
	}

	// Create API router (always available, even if services are not)
	apiRouter := api.NewRouter(containerManager, cfg)
	if containerManager != nil && dbService != nil {
		log.Println(" Container management API with database enabled")
	} else {
		log.Println(" Container management API mounted but some services unavailable")
	}

	// Get emulator address from environment or use default
	emulatorAddr := os.Getenv("EMULATOR_ADDR")
	if emulatorAddr == "" {
		emulatorAddr = "localhost:8554"
	}
	log.Printf(" Emulator address: %s", emulatorAddr)

	// Try to connect to emulator (optional - may not be running)
	emuClient, err = emulator.NewEmulatorClient(emulatorAddr)
	if err != nil {
		log.Printf(" Emulator not connected: %v", err)
		log.Println(" WebRTC streaming will be available when emulator connects")
	} else {
		log.Printf(" Connected to emulator gRPC at %s", emulatorAddr)
	}

	// Create HTTP mux
	mux := http.NewServeMux()

	// Mount API routes if container manager is available
	if apiRouter != nil {
		mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
			apiRouter.ServeHTTP(w, r)
		})
		// Handle offer routes through API router with JWT authentication
		mux.HandleFunc("/offer", api.ChainMiddleware(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiRouter.ServeHTTP(w, r)
			}),
			api.JWTMiddleware,
		).ServeHTTP)
		mux.HandleFunc("/offer/", api.ChainMiddleware(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiRouter.ServeHTTP(w, r)
			}),
			api.JWTMiddleware,
		).ServeHTTP)
	}

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "healthy",
			"version": "1.0.0",
		})
	})

	// DEPRECATED: Old frame loop - replaced by GPU bridge architecture
	// The bridge now runs inside containers with hardware encoding
	// go startFrameLoop()
	log.Println("🎬 GPU bridge streaming enabled - Go server handles signaling only")

	// Apply middleware
	handler := api.ChainMiddleware(
		mux,
		api.RecoveryMiddleware,
		api.LoggingMiddleware,
		api.CORSMiddleware(cfg.Server.AllowedOrigins),
	)

	serverAddr := ":" + cfg.Server.Port
	log.Printf(" Server listening on %s", serverAddr)
	// log.Println("📖 API Endpoints:")
	// log.Println("   GET  /health              - Health check")
	// log.Println("   GET  /api/health          - API health check")
	// log.Println("   GET  /api/images          - List available emulator images")
	// log.Println("   GET  /api/containers      - List containers")
	// log.Println("   POST /api/containers      - Create container")
	// log.Println("   GET  /api/containers/{id} - Get container details")
	// log.Println("   POST /api/containers/{id}/stop   - Stop container")
	// log.Println("   DELETE /api/containers/{id}      - Delete container")
	// log.Println("   GET  /api/containers/{id}/connect - Get connection info")
	// log.Println("   POST /offer               - WebRTC offer (default emulator)")
	// log.Println("   POST /offer/{container_id} - WebRTC offer (specific container)")

	log.Fatal(http.ListenAndServe(serverAddr, handler))
}

func handleInputCommand(cmd string) {
	if emuClient == nil {
		log.Printf(" No emulator client for input: %s", cmd)
		return
	}

	parts := strings.Split(cmd, ":")
	if len(parts) < 2 {
		return
	}

	cmdType := parts[0]
	args := parts[1:]

	switch cmdType {
	case "touch":
		if len(args) >= 2 {
			x, errX := strconv.Atoi(args[0])
			y, errY := strconv.Atoi(args[1])
			if errX == nil && errY == nil {
				touchEvent := &pb.TouchEvent{
					Touches: []*pb.Touch{{
						X: int32(x), Y: int32(y),
						Pressure: 50, TouchMajor: 10, TouchMinor: 10,
					}},
				}
				emuClient.SendTouch(touchEvent)
			}
		}
	case "swipe":
		if len(args) >= 5 {
			startX, _ := strconv.Atoi(args[0])
			startY, _ := strconv.Atoi(args[1])
			endX, _ := strconv.Atoi(args[2])
			endY, _ := strconv.Atoi(args[3])
			duration, _ := strconv.Atoi(args[4])

			if duration > 0 {
				steps := 12
				stepDuration := duration / steps

				// Touch down
				emuClient.SendTouch(&pb.TouchEvent{
					Touches: []*pb.Touch{{
						X: int32(startX), Y: int32(startY),
						Pressure: 80, TouchMajor: 15, TouchMinor: 15, Identifier: 1,
					}},
				})
				time.Sleep(30 * time.Millisecond)

				// Swipe movement
				for i := 1; i <= steps; i++ {
					progress := float64(i) / float64(steps)
					currentX := startX + int(float64(endX-startX)*progress)
					currentY := startY + int(float64(endY-startY)*progress)

					emuClient.SendTouch(&pb.TouchEvent{
						Touches: []*pb.Touch{{
							X: int32(currentX), Y: int32(currentY),
							Pressure: 80, TouchMajor: 15, TouchMinor: 15, Identifier: 1,
						}},
					})
					time.Sleep(time.Duration(stepDuration) * time.Millisecond)
				}

				// Touch up
				emuClient.SendTouch(&pb.TouchEvent{
					Touches: []*pb.Touch{{
						X: int32(endX), Y: int32(endY),
						Pressure: 0, TouchMajor: 15, TouchMinor: 15, Identifier: 1,
					}},
				})
			}
		}
	case "key":
		if len(args) >= 1 {
			if keyCode, err := strconv.Atoi(args[0]); err == nil {
				emuClient.SendKey(&pb.KeyboardEvent{
					CodeType:  pb.KeyboardEvent_Evdev,
					EventType: pb.KeyboardEvent_keypress,
					KeyCode:   int32(keyCode),
				})
			} else {
				emuClient.SendKey(&pb.KeyboardEvent{
					CodeType:  pb.KeyboardEvent_Evdev,
					EventType: pb.KeyboardEvent_keypress,
					Key:       args[0],
				})
			}
		}
	case "text":
		if len(args) >= 1 {
			text := strings.Join(args, ":")
			for _, char := range text {
				emuClient.SendKey(&pb.KeyboardEvent{
					CodeType:  pb.KeyboardEvent_Evdev,
					EventType: pb.KeyboardEvent_keypress,
					Text:      string(char),
				})
			}
		}
	}
}

// DEPRECATED: startFrameLoop - Legacy global frame loop
// This function is no longer used. The GPU bridge architecture handles
// video streaming inside individual containers with hardware encoding.
//
// The new architecture:
// - Emulator renders to virtio-gpu framebuffer
// - ffmpeg inside container captures and encodes with VAAPI/NVENC
// - H.264 NALs sent directly to WebRTC
// - Go server only handles signaling, not pixels
//
// TODO: Remove this function after full migration validation
func startFrameLoop() {
	const fps = 15 // Target FPS
	frameDuration := time.Second / fps
	frameCount := 0
	lastLogTime := time.Now()
	consecutiveErrors := 0

	log.Printf(" Frame loop started @ %d fps", fps)

	var lastImg *image.RGBA

	for {
		mu.Lock()
		bc := broadcaster
		mu.Unlock()

		if bc == nil || emuClient == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		frameStart := time.Now()

		// Get frame with timeout
		frameCh := make(chan image.Image, 1)
		errCh := make(chan error, 1)

		go func() {
			img, err := emuClient.GetFrame()
			if err != nil {
				errCh <- err
				return
			}
			frameCh <- img
		}()

		var img image.Image
		select {
		case img = <-frameCh:
			consecutiveErrors = 0
		case err := <-errCh:
			consecutiveErrors++
			if consecutiveErrors%10 == 1 {
				log.Printf(" GetFrame error: %v", err)
			}
			time.Sleep(frameDuration)
			continue
		case <-time.After(30 * time.Second):
			log.Printf(" GetFrame timeout")
			time.Sleep(2 * time.Second)
			continue
		}

		if img == nil {
			time.Sleep(frameDuration)
			continue
		}

		// Convert to RGBA if needed
		rgba, ok := img.(*image.RGBA)
		if !ok {
			bounds := img.Bounds()
			if lastImg == nil || lastImg.Bounds() != bounds {
				lastImg = image.NewRGBA(bounds)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					lastImg.Set(x, y, img.At(x, y))
				}
			}
			rgba = lastImg
		}

		// Initialize encoder on first frame
		if h264Enc == nil {
			bounds := rgba.Bounds()
			w, h := bounds.Dx(), bounds.Dy()
			var encErr error
			h264Enc, encErr = emulator.NewH264Encoder(w, h, fps)
			if encErr != nil {
				log.Printf(" Failed to create H264 encoder: %v", encErr)
				time.Sleep(time.Second)
				continue
			}
			log.Printf(" H.264 encoder initialized: %dx%d @ %d fps", w, h, fps)
		}

		// Encode frame
		h264Data, err := h264Enc.Encode(rgba)
		if err != nil {
			log.Printf(" H264 Encode error: %v", err)
			time.Sleep(frameDuration)
			continue
		}

		if len(h264Data) == 0 {
			continue // Encoder still processing
		}
		nals := emulator.ParseNALUnits(h264Data)
		// Send frame
		if err := bc.SendH264NALs(nals); err != nil {
			errStr := err.Error()
			if strings.Contains(errStr, "connection") || strings.Contains(errStr, "closed") {
				mu.Lock()
				broadcaster = nil
				mu.Unlock()
			}
		} else {
			frameCount++
			// Log stats every 5 seconds
			if time.Since(lastLogTime) > 5*time.Second {
				log.Printf(" Frames sent: %d, FPS: %.1f", frameCount, float64(frameCount)/time.Since(lastLogTime).Seconds())
				frameCount = 0
				lastLogTime = time.Now()
			}
		}

		// Maintain frame rate
		elapsed := time.Since(frameStart)
		if elapsed < frameDuration {
			time.Sleep(frameDuration - elapsed)
		}
	}
}

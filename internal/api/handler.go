package api

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"android_cloud_backend/internal/config"
	"android_cloud_backend/internal/container"
	"android_cloud_backend/internal/emulator"
	emulatorpb "android_cloud_backend/internal/emulator/proto"
	"android_cloud_backend/internal/models"

	"sync"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// Handler holds dependencies for API handlers
type Handler struct {
	containerManager *container.Manager
	config           *config.Config
}

// NewHandler creates a new API handler
func NewHandler(cm *container.Manager, cfg *config.Config) *Handler {
	return &Handler{
		containerManager: cm,
		config:           cfg,
	}
}


// HealthCheck handles GET /api/health
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	response := models.HealthResponse{
		Status:    "healthy",
		Version:   "1.0.0",
		Timestamp: time.Now(),
	}
	json.NewEncoder(w).Encode(response)
}

// --- Container Management Endpoints ---

// ListImages handles GET /api/images
func (h *Handler) ListImages(w http.ResponseWriter, r *http.Request) {
	images := container.AvailableImages(h.config.Docker.Registry)

	response := models.ListImagesResponse{
		Success: true,
		Images:  images,
	}
	json.NewEncoder(w).Encode(response)
}

// CreateContainer handles POST /api/containers
func (h *Handler) CreateContainer(w http.ResponseWriter, r *http.Request) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from context (set by middleware)
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	var req models.CreateContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid request body", "INVALID_REQUEST")
		return
	}

	// Validate required fields
	if req.ImageID == "" {
		h.sendError(w, http.StatusBadRequest, "image_id is required", "MISSING_IMAGE_ID")
		return
	}

	// Validate image exists
	if container.GetImageByID(h.config.Docker.Registry, req.ImageID) == nil {
		h.sendError(w, http.StatusBadRequest, "Unknown image ID: "+req.ImageID, "INVALID_IMAGE_ID")
		return
	}

	// Set user_id from context
	req.UserID = userID

	log.Printf("🆕 Creating container for user %s with image %s", userID, req.ImageID)

	ec, err := h.containerManager.CreateContainer(r.Context(), &req)
	if err != nil {
		log.Printf("Failed to create container: %v", err)
		h.sendError(w, http.StatusInternalServerError, err.Error(), "CREATE_FAILED")
		return
	}

	if ec == nil {
		log.Printf("Container creation returned nil container")
		h.sendError(w, http.StatusInternalServerError, "Container creation failed: nil response", "CREATE_FAILED")
		return
	}

	log.Printf("Container created successfully: ID=%s, Name=%s, Status=%s", ec.ID, ec.Name, ec.Status)
	log.Printf("📊 Container details: StartedAt=%v, CreatedAt=%v, UserID=%s", ec.StartedAt, ec.CreatedAt, ec.UserID)

	response := models.CreateContainerResponse{
		Success:   true,
		Container: ec,
	}
	w.WriteHeader(http.StatusCreated)
	log.Printf("Sending response for container: %s", ec.ID)

	// Try to marshal to check for issues
	if jsonData, err := json.Marshal(response); err != nil {
		log.Printf("JSON marshal error: %v", err)
		h.sendError(w, http.StatusInternalServerError, "Failed to serialize response", "MARSHAL_ERROR")
		return
	} else {
		log.Printf("JSON marshaled successfully, size: %d bytes", len(jsonData))
		w.Write(jsonData)
	}

	log.Printf("Response sent successfully for container: %s", ec.ID)
}

// ListContainers handles GET /api/containers
func (h *Handler) ListContainers(w http.ResponseWriter, r *http.Request) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from JWT context
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	statusFilter := r.URL.Query().Get("status")

	containers, err := h.containerManager.ListContainers(r.Context(), userID, statusFilter)
	if err != nil {
		log.Printf("Failed to list containers: %v", err)
		h.sendError(w, http.StatusInternalServerError, err.Error(), "LIST_FAILED")
		return
	}

	response := models.ListContainersResponse{
		Success:    true,
		Containers: containers,
		Total:      len(containers),
	}
	json.NewEncoder(w).Encode(response)
}

// GetContainer handles GET /api/containers/{id}
func (h *Handler) GetContainer(w http.ResponseWriter, r *http.Request) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from JWT context
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	containerID := h.extractPathParam(r, "/api/containers/")
	if containerID == "" {
		h.sendError(w, http.StatusBadRequest, "Container ID is required", "MISSING_CONTAINER_ID")
		return
	}

	// Handle sub-routes
	if strings.Contains(containerID, "/") {
		parts := strings.SplitN(containerID, "/", 2)
		containerID = parts[0]
		action := parts[1]

		switch action {
		case "connect":
			h.ConnectContainer(w, r, containerID)
			return
		}
	}

	ec, err := h.containerManager.GetContainer(r.Context(), containerID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}

	// Validate ownership
	if ec.UserID != userID {
		h.sendError(w, http.StatusForbidden, "Access denied", "FORBIDDEN")
		return
	}

	response := models.GetContainerResponse{
		Success:   true,
		Container: ec,
	}
	json.NewEncoder(w).Encode(response)
}

// StopContainer handles POST /api/containers/{id}/stop
func (h *Handler) StopContainer(w http.ResponseWriter, r *http.Request) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from JWT context
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	containerID := h.extractPathParam(r, "/api/containers/")
	containerID = strings.TrimSuffix(containerID, "/stop")

	if containerID == "" {
		h.sendError(w, http.StatusBadRequest, "Container ID is required", "MISSING_CONTAINER_ID")
		return
	}

	// Check ownership before stopping
	ec, err := h.containerManager.GetContainer(r.Context(), containerID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "Container not found", "NOT_FOUND")
		return
	}
	if ec.UserID != userID {
		h.sendError(w, http.StatusForbidden, "Access denied", "FORBIDDEN")
		return
	}

	var req models.StopContainerRequest
	json.NewDecoder(r.Body).Decode(&req) // Timeout is optional

	timeout := req.Timeout
	if timeout == 0 {
		timeout = 10
	}

	log.Printf("Stopping container: %s for user %s", containerID, userID)

	if err := h.containerManager.StopContainer(r.Context(), containerID, timeout); err != nil {
		log.Printf("Failed to stop container: %v", err)
		h.sendError(w, http.StatusInternalServerError, err.Error(), "STOP_FAILED")
		return
	}

	response := models.StopContainerResponse{
		Success: true,
	}
	json.NewEncoder(w).Encode(response)
}

// DeleteContainer handles DELETE /api/containers/{id}
func (h *Handler) DeleteContainer(w http.ResponseWriter, r *http.Request) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from JWT context
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	containerID := h.extractPathParam(r, "/api/containers/")
	if containerID == "" {
		h.sendError(w, http.StatusBadRequest, "Container ID is required", "MISSING_CONTAINER_ID")
		return
	}

	// Check ownership before deleting
	ec, err := h.containerManager.GetContainer(r.Context(), containerID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, "Container not found", "NOT_FOUND")
		return
	}
	if ec.UserID != userID {
		h.sendError(w, http.StatusForbidden, "Access denied", "FORBIDDEN")
		return
	}

	// Check for force parameter
	force := r.URL.Query().Get("force") == "true"

	log.Printf("Deleting container: %s for user %s (force: %v)", containerID, userID, force)

	if err := h.containerManager.DeleteContainer(r.Context(), containerID, force); err != nil {
		log.Printf("Failed to delete container: %v", err)
		h.sendError(w, http.StatusInternalServerError, err.Error(), "DELETE_FAILED")
		return
	}

	response := models.DeleteContainerResponse{
		Success: true,
	}
	json.NewEncoder(w).Encode(response)
}

// ConnectContainer handles GET /api/containers/{id}/connect
func (h *Handler) ConnectContainer(w http.ResponseWriter, r *http.Request, containerID string) {
	// Check if container manager is available
	if h.containerManager == nil {
		h.sendError(w, http.StatusServiceUnavailable, "Container management is not available (Docker not connected)", "DOCKER_UNAVAILABLE")
		return
	}

	// Get user_id from JWT context
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	if containerID == "" {
		containerID = h.extractPathParam(r, "/api/containers/")
		containerID = strings.TrimSuffix(containerID, "/connect")
	}

	if containerID == "" {
		h.sendError(w, http.StatusBadRequest, "Container ID is required", "MISSING_CONTAINER_ID")
		return
	}

	ec, err := h.containerManager.GetContainer(r.Context(), containerID)
	if err != nil {
		h.sendError(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
		return
	}

	// Validate ownership
	if ec.UserID != userID {
		h.sendError(w, http.StatusForbidden, "Access denied", "FORBIDDEN")
		return
	}

	if ec.Status != models.StatusRunning {
		h.sendError(w, http.StatusBadRequest, "Container is not running", "CONTAINER_NOT_RUNNING")
		return
	}

	response := models.ConnectEmulatorResponse{
		Success:     true,
		GRPCAddress: "localhost:" + itoa(ec.GRPCPort),
	}

	if ec.ADBPort > 0 {
		response.ADBAddress = "localhost:" + itoa(ec.ADBPort)
	}

	json.NewEncoder(w).Encode(response)
}

// --- Helper Functions ---

func (h *Handler) extractPathParam(r *http.Request, prefix string) string {
	return strings.TrimPrefix(r.URL.Path, prefix)
}

func (h *Handler) sendError(w http.ResponseWriter, statusCode int, message string, code string) {
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(models.ErrorResponse{
		Success: false,
		Error:   message,
		Code:    code,
	})
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// --- WebRTC Offer Endpoints ---

// HandleOffer handles WebRTC offers for the default emulator
func (h *Handler) HandleOffer(w http.ResponseWriter, r *http.Request) {
	h.handleOffer(w, r, "")
}

// HandleOfferWithContainer handles WebRTC offers for specific containers
func (h *Handler) HandleOfferWithContainer(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Extract container ID from path
	containerID := strings.TrimPrefix(r.URL.Path, "/offer/")
	h.handleOffer(w, r, containerID)
}

// WebRTC session manager for persistent connections
type WebRTCSession struct {
	peerConnection *webrtc.PeerConnection
	videoTrack     *webrtc.TrackLocalStaticSample
	emulatorClient *emulator.EmulatorClient
	encoder        interface{} // Can be H264Encoder or VP8Encoder
	codecType      string      // "h264" or "vp8"
	broadcaster    *emulator.Broadcaster
	containerID    string
	grpcPort       int
	lastActivity   time.Time
}

type WebRTCSessionManager struct {
	sessions map[string]*WebRTCSession
	mutex    sync.RWMutex
}

var sessionManager = &WebRTCSessionManager{
	sessions: make(map[string]*WebRTCSession),
}

func (sm *WebRTCSessionManager) addSession(containerID string, session *WebRTCSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.sessions[containerID] = session
	log.Printf("Added WebRTC session for container %s", containerID)
}

func (sm *WebRTCSessionManager) removeSession(containerID string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	if session, exists := sm.sessions[containerID]; exists {
		if session.peerConnection != nil {
			session.peerConnection.Close()
		}
		if session.emulatorClient != nil {
			session.emulatorClient.Close()
		}
		// Close encoder if it has a Close method
		if session.encoder != nil {
			if encoder, ok := session.encoder.(*emulator.H264Encoder); ok {
				encoder.Close()
			}
		}
		delete(sm.sessions, containerID)
		log.Printf("Removed WebRTC session for container %s", containerID)
	}
}

func (sm *WebRTCSessionManager) getSession(containerID string) *WebRTCSession {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	return sm.sessions[containerID]
}

func (h *Handler) handleOffer(w http.ResponseWriter, r *http.Request, containerID string) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get user ID
	userID, ok := r.Context().Value("user_id").(string)
	if !ok || userID == "" {
		h.sendError(w, http.StatusUnauthorized, "User not authenticated", "UNAUTHENTICATED")
		return
	}

	// Validate container ownership if containerID provided
	var targetContainer *models.EmulatorContainer
	if containerID != "" {
		containers, err := h.containerManager.ListContainers(r.Context(), userID, "")
		if err != nil {
			h.sendError(w, http.StatusInternalServerError, "Failed to list containers", "LIST_FAILED")
			return
		}
		for _, c := range containers {
			if c.ID == containerID {
				targetContainer = c
				break
			}
		}
		if targetContainer == nil {
			h.sendError(w, http.StatusNotFound, "Container not found", "NOT_FOUND")
			return
		}
		if targetContainer.Status != models.StatusRunning {
			h.sendError(w, http.StatusBadRequest, "Container is not running", "CONTAINER_NOT_RUNNING")
			return
		}
	}

	// Read offer
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "Failed to read request body", "READ_ERROR")
		return
	}

	var offer webrtc.SessionDescription
	if err := json.Unmarshal(raw, &offer); err != nil {
		h.sendError(w, http.StatusBadRequest, "Invalid WebRTC offer", "INVALID_OFFER")
		return
	}

	// Create WebRTC peer connection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to create peer connection", "WEBRTC_ERROR")
		return
	}

	// Create H.264 video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		},
		"video",
		fmt.Sprintf("emulator-%s", containerID),
	)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to create video track", "TRACK_ERROR")
		return
	}

	// *** CRITICAL FIX: ADD TRANSCIEVER ***
	_, err = peerConnection.AddTransceiverFromTrack(
		videoTrack,
		webrtc.RtpTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionSendonly,
		},
	)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to add video transceiver", "TRANSCEIVER_ERROR")
		return
	}

	// Apply remote description (offer)
	if err := peerConnection.SetRemoteDescription(offer); err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to set remote description", "REMOTE_DESC_ERROR")
		return
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to create answer", "CREATE_ANSWER_ERROR")
		return
	}

	// Prepare session
	grpcPort := 0
	if targetContainer != nil {
		grpcPort = targetContainer.GRPCPort
	}

	session := &WebRTCSession{
		peerConnection: peerConnection,
		videoTrack:     videoTrack,
		containerID:    containerID,
		grpcPort:       grpcPort,
		codecType:      "h264",
		lastActivity:   time.Now(),
	}

	// Handle data channels (for touch input)
	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf("📡 Data channel opened: %s", dc.Label())

		dc.OnOpen(func() {
			log.Printf("Data channel ready: %s", dc.Label())
		})

		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if !msg.IsString {
				return
			}

			message := string(msg.Data)
			log.Printf("📨 Received message on %s: %s", dc.Label(), message)

			// Handle touch commands
			if strings.HasPrefix(message, "touch:") {
				parts := strings.Split(message, ":")
				if len(parts) == 3 {
					x, errX := strconv.Atoi(parts[1])
					y, errY := strconv.Atoi(parts[2])
					if errX == nil && errY == nil {
						log.Printf("👆 Processing touch at (%d, %d) for container %s", x, y, containerID)
						// TODO: Send touch to emulator via gRPC
						go session.sendTouchToEmulator(x, y)
					}
				}
			}
		})

		dc.OnClose(func() {
			log.Printf("Data channel closed: %s", dc.Label())
		})
	})

	// Connection state callback
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("WebRTC state for %s: %s", containerID, state.String())

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			sessionManager.removeSession(containerID)
			return
		}

		if state == webrtc.PeerConnectionStateConnected {
			if session.grpcPort > 0 {
				go session.startStreaming(session.grpcPort)
			} else {
				go session.sendTestFrames()
			}
		}
	})

	// Set local answer
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		h.sendError(w, http.StatusInternalServerError, "Failed to set local description", "LOCAL_DESC_ERROR")
		return
	}

	// Wait for ICE
	select {
	case <-webrtc.GatheringCompletePromise(peerConnection):
	case <-time.After(10 * time.Second):
		log.Printf("ICE gathering timeout for %s", containerID)
	}

	// Save session
	sessionManager.addSession(containerID, session)

	// Return answer
	local := peerConnection.LocalDescription()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": local.Type.String(),
		"sdp":  local.SDP,
	})
}

// sendTouchToEmulator sends a touch event to the emulator
func (s *WebRTCSession) sendTouchToEmulator(x, y int) {
	if s.grpcPort <= 0 {
		log.Printf("Cannot send touch - no gRPC port for session %s", s.containerID)
		return
	}

	// Create emulator client if needed
	if s.emulatorClient == nil {
		addr := fmt.Sprintf("localhost:%d", s.grpcPort)
		client, err := emulator.NewEmulatorClient(addr)
		if err != nil {
			log.Printf("Failed to create emulator client for touch: %v", err)
			return
		}
		s.emulatorClient = client
	}

	// Send touch down event
	touchDownEvent := &emulatorpb.TouchEvent{
		Touches: []*emulatorpb.Touch{{
			X:          int32(x),
			Y:          int32(y),
			Pressure:   50,
			TouchMajor: 10,
			TouchMinor: 10,
		}},
	}

	if err := s.emulatorClient.SendTouch(touchDownEvent); err != nil {
		log.Printf("Failed to send touch down to emulator %s: %v", s.containerID, err)
		return
	}

	log.Printf("Touch down sent to emulator %s at (%d, %d)", s.containerID, x, y)

	// Send touch up event after a short delay (simulate tap)
	go func() {
		time.Sleep(100 * time.Millisecond)

		touchUpEvent := &emulatorpb.TouchEvent{
			Touches: []*emulatorpb.Touch{{
				X:          int32(x),
				Y:          int32(y),
				Pressure:   0, // 0 pressure = touch up
				TouchMajor: 10,
				TouchMinor: 10,
			}},
		}

		if err := s.emulatorClient.SendTouch(touchUpEvent); err != nil {
			log.Printf("Failed to send touch up to emulator %s: %v", s.containerID, err)
		} else {
			log.Printf("Touch up sent to emulator %s at (%d, %d)", s.containerID, x, y)
		}
	}()
}

// streamEmulatorFrames captures frames from the emulator, encodes them to H.264, and sends them to WebRTC video track
// WebRTC session methods
func (s *WebRTCSession) startStreaming(grpcPort int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic recovered in startStreaming: %v", r)
		}
	}()

	// Store gRPC port
	s.grpcPort = grpcPort
	addr := fmt.Sprintf("localhost:%d", grpcPort)
	log.Printf("🎮 Connecting to emulator at %s for session %s", addr, s.containerID)

	// Connect to emulator
	client, err := emulator.NewEmulatorClient(addr)
	if err != nil {
		log.Printf("Failed to connect to emulator: %v", err)
		sessionManager.removeSession(s.containerID)
		return
	}
	s.emulatorClient = client
	log.Printf("Connected to emulator for session %s", s.containerID)

	// Fetch first frame
	firstFrame, err := s.emulatorClient.GetFrame()
	if err != nil {
		log.Printf("Failed to get initial frame: %v", err)
		sessionManager.removeSession(s.containerID)
		return
	}

	width := firstFrame.Bounds().Dx()
	height := firstFrame.Bounds().Dy()
	log.Printf("Initial frame size: %dx%d for session %s", width, height, s.containerID)

	// Create H.264 encoder (30 FPS)
	enc, err := emulator.NewH264Encoder(width, height, 30)
	if err != nil {
		log.Printf("Failed creating H264 encoder: %v", err)
		sessionManager.removeSession(s.containerID)
		return
	}
	s.encoder = enc
	log.Printf("Created H264 encoder for %s", s.containerID)

	// Create broadcaster
	s.broadcaster = emulator.NewBroadcaster(s.videoTrack)

	// Encode AND send the very first frame (with SPS/PPS if available)
	h264enc, ok := s.encoder.(*emulator.H264Encoder)
	if !ok {
		log.Printf("Encoder is not H264Encoder")
		sessionManager.removeSession(s.containerID)
		return
	}

	nals, err := h264enc.EncodeWithNALs(firstFrame)
	if err != nil {
		log.Printf("Failed to encode first frame: %v", err)
	} else if len(nals) > 0 {
		log.Printf("🎬 Sending first frame (%d NAL units)", len(nals))

		// Try twice (sometimes the first write fails due to ICE not complete)
		if err := s.broadcaster.SendH264NALs(nals); err != nil {
			log.Printf("First send failed, retrying: %v", err)

			time.Sleep(150 * time.Millisecond)
			if err2 := s.broadcaster.SendH264NALs(nals); err2 != nil {
				log.Printf("Failed to send initial frame on retry: %v", err2)
			} else {
				log.Printf("Initial frame sent on retry")
			}
		} else {
			log.Printf("Initial frame sent successfully")
		}
	}

	// START STREAMING LOOP IN NEW GOROUTINE (critical)
	go s.streamingLoop()
}

func (s *WebRTCSession) sendTestFrames() {
	log.Printf("🧪 Sending test frames for session %s", s.containerID)

	// Send test H.264 frames every second
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	frameCount := 0
	for range ticker.C {
		// Check if session is still active
		if sessionManager.getSession(s.containerID) == nil {
			log.Printf("Test stream ended for session %s", s.containerID)
			break
		}

		// Send a minimal H.264 frame
		testData := []byte{
			0x00, 0x00, 0x00, 0x01, // Start code
			0x67, 0x42, 0x00, 0x0A, // SPS data
			0xF8, 0x41, 0xA2, // More SPS data
		}

		if err := s.videoTrack.WriteSample(media.Sample{
			Data:     testData,
			Duration: time.Second / 30,
		}); err != nil {
			log.Printf("Failed to send test frame %d: %v", frameCount, err)
		} else {
			log.Printf("Sent test frame %d for session %s", frameCount, s.containerID)
		}

		frameCount++
		if frameCount >= 30 { // Stop after 30 seconds
			sessionManager.removeSession(s.containerID)
			break
		}
	}
}

func (s *WebRTCSession) streamingLoop() {
	log.Printf("Starting streaming loop for session %s at 10 FPS", s.containerID)

	ticker := time.NewTicker(33 * time.Millisecond) // 30 FPS
	defer ticker.Stop()

	frameCount := 0
	consecutiveErrors := 0
	maxConsecutiveErrors := 20

	encoder, ok := s.encoder.(*emulator.H264Encoder)
	if !ok {
		log.Printf("Encoder is not H.264 encoder")
		return
	}

	for range ticker.C {
		// Stop if session removed
		if sessionManager.getSession(s.containerID) == nil {
			log.Printf("Streaming ended for session %s", s.containerID)
			return
		}

		// Pull a new frame from emulator
		frame, err := s.getFrameWithTimeout(3 * time.Second)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				log.Printf("Too many frame errors — restarting emulator connection")
				if err := s.restartEmulatorConnection(); err != nil {
					log.Printf("Restart failed: %v", err)
					return
				}
				consecutiveErrors = 0
			}
			continue
		}
		consecutiveErrors = 0

		// Encode frame to H.264 NAL units
		nals, err := encoder.EncodeWithNALs(frame)
		if err != nil {
			log.Printf("Failed to encode H.264 frame: %v", err)
			continue
		}

		// Send NALs to WebRTC via broadcaster
		if len(nals) > 0 && s.broadcaster != nil {
			totalBytes := 0
			for _, nal := range nals {
				totalBytes += len(nal)
			}

			if err := s.broadcaster.SendH264NALs(nals); err != nil {
				log.Printf("Failed to send H.264 frame: %v", err)
				continue
			}

			if frameCount%50 == 0 { // Log every 5 seconds at 10 FPS
				log.Printf("📺 Streamed %d frames (%d bytes last) for session %s", frameCount, totalBytes, s.containerID)
			}
		} else if nals == nil && frameCount < 10 {
			log.Printf("⏳ Encoder warming up for frame %d", frameCount)
		}

		frameCount++
	}
}

// getFrameWithTimeout gets a frame with a custom timeout
func (s *WebRTCSession) getFrameWithTimeout(timeout time.Duration) (image.Image, error) {
	if s.emulatorClient == nil {
		return nil, fmt.Errorf("emulator client is nil")
	}

	// Use a channel to implement timeout
	frameCh := make(chan image.Image, 1)
	errCh := make(chan error, 1)

	go func() {
		frame, err := s.emulatorClient.GetFrame()
		if err != nil {
			errCh <- err
		} else {
			frameCh <- frame
		}
	}()

	select {
	case frame := <-frameCh:
		return frame, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("frame timeout after %v", timeout)
	}
}

// restartEmulatorConnection attempts to restart the emulator connection
func (s *WebRTCSession) restartEmulatorConnection() error {
	if s.emulatorClient != nil {
		s.emulatorClient.Close()
		s.emulatorClient = nil
	}

	addr := fmt.Sprintf("localhost:%d", s.grpcPort)
	log.Printf("Restarting emulator connection for session %s at %s", s.containerID, addr)

	var err error
	s.emulatorClient, err = emulator.NewEmulatorClient(addr)
	if err != nil {
		return fmt.Errorf("failed to reconnect to emulator: %w", err)
	}

	log.Printf("Reconnected to emulator for session %s", s.containerID)
	return nil
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"image" // TODO: Remove after full migration to bridge
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"android_cloud_backend/internal/bridge"
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

	log.Printf("Creating container for user %s with image %s", userID, req.ImageID)

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
	log.Printf("Container details: StartedAt=%v, CreatedAt=%v, UserID=%s", ec.StartedAt, ec.CreatedAt, ec.UserID)

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

// StartContainer handles POST /api/containers/{id}/start
func (h *Handler) StartContainer(w http.ResponseWriter, r *http.Request) {
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

	// Extract container ID from URL path
	containerID := strings.TrimPrefix(r.URL.Path, "/api/containers/")
	containerID = strings.TrimSuffix(containerID, "/start")

	if containerID == "" {
		h.sendError(w, http.StatusBadRequest, "Container ID required", "MISSING_CONTAINER_ID")
		return
	}

	log.Printf("Starting container: %s for user %s", containerID, userID)

	if err := h.containerManager.StartContainer(r.Context(), containerID); err != nil {
		log.Printf("Failed to start container: %v", err)
		h.sendError(w, http.StatusInternalServerError, err.Error(), "START_FAILED")
		return
	}

	response := map[string]interface{}{
		"success": true,
		"message": "Container started successfully",
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

// Encoder interface for different video encoders
type Encoder interface {
	FPS() int
	IsBusy() bool
}

// WebRTC session manager for persistent connections
// Updated for GPU bridge architecture - Go no longer touches raw frames
type WebRTCSession struct {
	peerConnection   *webrtc.PeerConnection
	videoTrack       *webrtc.TrackLocalStaticSample
	emulatorClient   *emulator.EmulatorClient // Still used for touch/keyboard input
	encoder          Encoder                  // DEPRECATED: Will be removed after bridge migration
	codecType        string                   // "h264" or "vp8"
	broadcaster      *emulator.Broadcaster    // DEPRECATED: Will be removed after bridge migration
	containerID      string
	grpcPort         int
	lastActivity     time.Time
	containerBridge  *bridge.ContainerBridge // NEW: GPU bridge for streaming
	BridgeProcessID  string                  // NEW: Docker exec ID for the bridge process
	containerManager *container.Manager      // NEW: Reference to container manager
	useGPUBridge     bool                    // NEW: Flag to use GPU bridge instead of old pipeline
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
		// Stop GPU bridge if running
		if session.containerBridge != nil {
			session.containerBridge.Stop()
		}
		// Close encoder if it has a Close method (DEPRECATED)
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

	// Get TURN server configuration from environment or use defaults
	turnServer := os.Getenv("TURN_SERVER")
	if turnServer == "" {
		turnServer = "turn:turn.aether.dev:3478" // Default TURN server
	}
	turnUser := os.Getenv("TURN_USER")
	if turnUser == "" {
		turnUser = "dev"
	}
	turnPass := os.Getenv("TURN_PASS")
	if turnPass == "" {
		turnPass = "dev123"
	}

	// Create WebRTC peer connection with TURN server for NAT traversal
	// TURN is required for cloud instances behind NAT/firewall
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			// STUN for direct connectivity check
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			// TURN for relay when direct connection fails (essential for GCP)
			{
				URLs:       []string{turnServer, strings.Replace(turnServer, "turn:", "turns:", 1) + "?transport=tcp"},
				Username:   turnUser,
				Credential: turnPass,
			},
		},
		ICETransportPolicy: webrtc.ICETransportPolicyAll, // Try direct first, then relay
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

	// Prepare session with GPU bridge support
	grpcPort := 0
	if targetContainer != nil {
		grpcPort = targetContainer.GRPCPort
	}

	// Check if GPU bridge should be used
	// Set USE_GPU_BRIDGE=false on cloud instances without GPU (e.g., standard GCP VMs)
	// Set USE_GPU_BRIDGE=true on GPU instances or WSL2 with GPU passthrough
	useGPUBridge := os.Getenv("USE_GPU_BRIDGE") != "false"
	if os.Getenv("USE_GPU_BRIDGE") == "" {
		// Auto-detect: default to false if no GPU likely available
		// This helps cloud instances without GPU work out of the box
		useGPUBridge = true // Tries bridge first, falls back to gRPC if fails
	}

	session := &WebRTCSession{
		peerConnection:   peerConnection,
		videoTrack:       videoTrack,
		containerID:      containerID,
		grpcPort:         grpcPort,
		codecType:        "h264",
		lastActivity:     time.Now(),
		containerManager: h.containerManager,
		useGPUBridge:     useGPUBridge,
	}

	// Handle data channels (for touch input)
	peerConnection.OnDataChannel(func(dc *webrtc.DataChannel) {
		log.Printf(" Data channel opened: %s", dc.Label())

		dc.OnOpen(func() {
			log.Printf("Data channel ready: %s", dc.Label())
		})

		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if !msg.IsString {
				return
			}

			message := string(msg.Data)
			log.Printf(" Received message on %s: %s", dc.Label(), message)

			// Handle touch commands
			if strings.HasPrefix(message, "touch:") {
				parts := strings.Split(message, ":")
				if len(parts) == 3 {
					x, errX := strconv.Atoi(parts[1])
					y, errY := strconv.Atoi(parts[2])
					if errX == nil && errY == nil {
						log.Printf(" Processing touch at (%d, %d) for container %s", x, y, containerID)
						go session.sendTouchToEmulator(x, y)
					}
				}
			}

			// Handle keyboard commands
			if strings.HasPrefix(message, "keydown:") || strings.HasPrefix(message, "keyup:") {
				parts := strings.Split(message, ":")
				if len(parts) == 3 {
					eventType := strings.TrimPrefix(parts[0], "key")
					keyName := parts[1]
					keyCode := parts[2]

					log.Printf(" Processing keyboard %s: %s (%s) for container %s", eventType, keyName, keyCode, containerID)
					go session.sendKeyToEmulator(eventType, keyName, keyCode)
				}
			}
		})

		dc.OnClose(func() {
			log.Printf("Data channel closed: %s", dc.Label())
		})
	})

	// Connection state callback - GPU bridge architecture
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("WebRTC state for %s: %s", containerID, state.String())

		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			sessionManager.removeSession(containerID)
			return
		}

		if state == webrtc.PeerConnectionStateConnected {
			// NEW: Try GPU bridge first (no pixel processing in Go)
			if session.useGPUBridge && session.containerManager != nil && session.containerID != "" {
				log.Printf("🚀 Starting GPU bridge for container %s", session.containerID)

				// Start the GPU bridge inside the container
				go func() {
					ctx := context.Background()

					// Create container bridge for streaming
					bridgeCfg := &bridge.ContainerBridgeConfig{
						ContainerID: session.containerID,
						DockerHost:  session.containerManager.GetDockerHost(),
						HTTPClient:  session.containerManager.GetHTTPClient(),
						FPS:         30,
						Width:       1280,
						Height:      720,
					}

					session.containerBridge = bridge.NewContainerBridge(session.videoTrack, bridgeCfg)

					if err := session.containerBridge.Start(ctx); err != nil {
						log.Printf("⚠️ GPU bridge failed, falling back to gRPC streaming: %v", err)
						// Fallback to old method (DEPRECATED)
						if session.grpcPort > 0 {
							go session.startStreaming(session.grpcPort)
						} else {
							go session.sendTestFrames()
						}
						return
					}

					log.Printf("✅ GPU bridge started for %s - Go is NOT processing pixels", session.containerID)
				}()
			} else if session.grpcPort > 0 {
				// DEPRECATED: Old streaming method (still available as fallback)
				log.Printf("⚠️ Using legacy gRPC streaming for container %s", containerID)
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

func (s *WebRTCSession) sendKeyToEmulator(eventType, keyName, keyCode string) {
	if s.grpcPort <= 0 {
		log.Printf(" Cannot send key - no gRPC port for session %s", s.containerID)
		return
	}

	// Create emulator client if needed
	if s.emulatorClient == nil {
		addr := fmt.Sprintf("localhost:%d", s.grpcPort)
		client, err := emulator.NewEmulatorClient(addr)
		if err != nil {
			log.Printf(" Failed to create emulator client for key input: %v", err)
			return
		}
		s.emulatorClient = client
	}

	// Map key names to Android key codes (simplified mapping)
	keyCodeMap := map[string]int32{
		"Enter":      66,  // KEYCODE_ENTER
		"Backspace":  67,  // KEYCODE_DEL
		"Tab":        61,  // KEYCODE_TAB
		"ArrowUp":    19,  // KEYCODE_DPAD_UP
		"ArrowDown":  20,  // KEYCODE_DPAD_DOWN
		"ArrowLeft":  21,  // KEYCODE_DPAD_LEFT
		"ArrowRight": 22,  // KEYCODE_DPAD_RIGHT
		"Escape":     111, // KEYCODE_ESCAPE
		" ":          62,  // KEYCODE_SPACE
		"a":          29,  // KEYCODE_A
		"b":          30,  // KEYCODE_B
		"c":          31,  // KEYCODE_C
		"d":          32,  // KEYCODE_D
		"e":          33,  // KEYCODE_E
		"f":          34,  // KEYCODE_F
		"g":          35,  // KEYCODE_G
		"h":          36,  // KEYCODE_H
		"i":          37,  // KEYCODE_I
		"j":          38,  // KEYCODE_J
		"k":          39,  // KEYCODE_K
		"l":          40,  // KEYCODE_L
		"m":          41,  // KEYCODE_M
		"n":          42,  // KEYCODE_N
		"o":          43,  // KEYCODE_O
		"p":          44,  // KEYCODE_P
		"q":          45,  // KEYCODE_Q
		"r":          46,  // KEYCODE_R
		"s":          47,  // KEYCODE_S
		"t":          48,  // KEYCODE_T
		"u":          49,  // KEYCODE_U
		"v":          50,  // KEYCODE_V
		"w":          51,  // KEYCODE_W
		"x":          52,  // KEYCODE_X
		"y":          53,  // KEYCODE_Y
		"z":          54,  // KEYCODE_Z
		"0":          7,   // KEYCODE_0
		"1":          8,   // KEYCODE_1
		"2":          9,   // KEYCODE_2
		"3":          10,  // KEYCODE_3
		"4":          11,  // KEYCODE_4
		"5":          12,  // KEYCODE_5
		"6":          13,  // KEYCODE_6
		"7":          14,  // KEYCODE_7
		"8":          15,  // KEYCODE_8
		"9":          16,  // KEYCODE_9
	}

	androidKeyCode, exists := keyCodeMap[keyName]
	if !exists {
		// For unmapped keys, try to use the numeric keyCode if it's a number
		if code, err := strconv.Atoi(keyCode); err == nil && code > 0 {
			androidKeyCode = int32(code)
		} else {
			log.Printf(" Unmapped key: %s (%s)", keyName, keyCode)
			return
		}
	}

	// Determine event type
	var eventTypeEnum emulatorpb.KeyboardEvent_KeyEventType
	if eventType == "down" {
		eventTypeEnum = emulatorpb.KeyboardEvent_keydown
	} else {
		eventTypeEnum = emulatorpb.KeyboardEvent_keyup
	}

	// Create keyboard event
	keyEvent := &emulatorpb.KeyboardEvent{
		CodeType:  emulatorpb.KeyboardEvent_Evdev, // Use Evdev key codes
		EventType: eventTypeEnum,
		KeyCode:   androidKeyCode,
		Key:       keyName,
	}

	if err := s.emulatorClient.SendKey(keyEvent); err != nil {
		log.Printf(" Failed to send key %s to emulator %s: %v", eventType, s.containerID, err)
	} else {
		log.Printf(" Key %s sent to emulator %s: %s (%d)", eventType, s.containerID, keyName, androidKeyCode)
	}
}

// DEPRECATED: startStreaming - Legacy frame streaming method
// This method is replaced by the GPU bridge architecture.
// The GPU bridge runs ffmpeg inside the container with hardware encoding (VAAPI/NVENC),
// eliminating the need for Go to process raw frames.
//
// This method is kept for backward compatibility when GPU bridge fails.
// TODO: Remove after GPU bridge is fully validated
func (s *WebRTCSession) startStreaming(grpcPort int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Panic recovered in startStreaming: %v", r)
		}
	}()

	// Store gRPC port
	s.grpcPort = grpcPort
	addr := fmt.Sprintf("localhost:%d", grpcPort)
	log.Printf(" Connecting to emulator at %s for session %s", addr, s.containerID)

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

	// Create H.264 encoder (15 FPS) to match emulator
	enc, err := emulator.NewH264Encoder(width, height, 15)
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
		log.Printf(" Sending first frame (%d NAL units)", len(nals))

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
	log.Printf(" Sending test frames for session %s", s.containerID)

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

// DEPRECATED: streamingLoop - Legacy frame processing loop
// This method processes raw frames from the emulator which is inefficient.
// The GPU bridge architecture handles encoding inside the container.
// TODO: Remove after GPU bridge is fully validated
func (s *WebRTCSession) streamingLoop() {
	log.Printf("Starting streaming loop for session %s at %d FPS", s.containerID, s.encoder.FPS())

	ticker := time.NewTicker(time.Second / time.Duration(s.encoder.FPS()))
	defer ticker.Stop()

	lastFrameTime := time.Now()
	frameCount := 0
	consecutiveErrors := 0
	maxConsecutiveErrors := 20

	// Encoder interface already validated

	for range ticker.C {
		// Stop if session removed
		if sessionManager.getSession(s.containerID) == nil {
			log.Printf("Streaming ended for session %s", s.containerID)
			return
		}

		// Add frame pacing to prevent sending frames too fast to FFmpeg
		elapsed := time.Since(lastFrameTime)
		frameInterval := time.Second / time.Duration(s.encoder.FPS())
		if elapsed < frameInterval {
			time.Sleep(frameInterval - elapsed)
		}
		lastFrameTime = time.Now()

		// Check encoder backpressure - skip frame if encoder is busy
		if s.encoder.IsBusy() {
			log.Printf("Encoder busy, skipping frame %d", frameCount)
			continue
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
		h264enc, ok := s.encoder.(*emulator.H264Encoder)
		if !ok {
			log.Printf("Encoder is not H.264 encoder")
			continue
		}
		nals, err := h264enc.EncodeWithNALs(frame)
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
				log.Printf(" Streamed %d frames (%d bytes last) for session %s", frameCount, totalBytes, s.containerID)
			}
		} else if nals == nil && frameCount < 10 {
			log.Printf(" Encoder warming up for frame %d", frameCount)
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

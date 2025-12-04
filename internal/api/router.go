package api

import (
	"net/http"
	"strings"

	"android_cloud_backend/internal/config"
	"android_cloud_backend/internal/container"
)

// Router handles HTTP routing for the API
type Router struct {
	handler *Handler
	mux     *http.ServeMux
	config  *config.Config
}

// NewRouter creates a new API router
func NewRouter(cm *container.Manager, cfg *config.Config) *Router {
	handler := NewHandler(cm, cfg)
	router := &Router{
		handler: handler,
		mux:     http.NewServeMux(),
		config:  cfg,
	}
	router.setupRoutes()
	return router
}

// setupRoutes configures all API routes
func (r *Router) setupRoutes() {
	// Health check
	r.mux.HandleFunc("/api/health", r.handler.HealthCheck)

	// Images
	r.mux.HandleFunc("/api/images", r.handler.ListImages)

	// Container management - handle all /api/containers/* routes
	r.mux.HandleFunc("/api/containers", r.handleContainers)
	r.mux.HandleFunc("/api/containers/", r.handleContainerByID)

	// WebRTC offer endpoints
	r.mux.HandleFunc("/offer", r.handler.HandleOffer)
	r.mux.HandleFunc("/offer/", r.handler.HandleOfferWithContainer)
}

// handleContainers routes /api/containers requests
func (r *Router) handleContainers(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		r.handler.ListContainers(w, req)
	case http.MethodPost:
		r.handler.CreateContainer(w, req)
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleContainerByID routes /api/containers/{id}/* requests
func (r *Router) handleContainerByID(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/api/containers/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Container ID required", http.StatusBadRequest)
		return
	}

	// containerID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "stop" && req.Method == http.MethodPost:
		r.handler.StopContainer(w, req)
	case action == "start" && req.Method == http.MethodPost:
		r.handler.StartContainer(w, req)
	case action == "connect" && req.Method == http.MethodGet:
		r.handler.ConnectContainer(w, req, "")
	case action == "" && req.Method == http.MethodGet:
		r.handler.GetContainer(w, req)
	case action == "" && req.Method == http.MethodDelete:
		r.handler.DeleteContainer(w, req)
	case req.Method == http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Handler returns the HTTP handler with all middleware applied
func (r *Router) Handler() http.Handler {
	return ChainMiddleware(
		r.mux,
		RecoveryMiddleware,
		LoggingMiddleware,
		ContentTypeMiddleware,
		CORSMiddleware(r.config.Server.AllowedOrigins),
		JWTMiddleware,
	)
}

// ServeHTTP implements http.Handler
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.Handler().ServeHTTP(w, req)
}

package models

import "time"

// EmulatorImage represents an available Android emulator image
type EmulatorImage struct {
	ID          string `json:"id"`          // e.g., "30-google-x64"
	Name        string `json:"name"`        // Human-readable name
	APILevel    int    `json:"api_level"`   // Android API level (e.g., 30)
	AndroidVer  string `json:"android_ver"` // Android version (e.g., "11")
	Variant     string `json:"variant"`     // aosp, google, playstore
	ABI         string `json:"abi"`         // x86, x86_64, arm64-v8a
	Tag         string `json:"tag"`         // Docker tag
	FullImage   string `json:"full_image"`  // Full image path
}

// ContainerStatus represents the current state of a container
type ContainerStatus string

const (
	StatusCreating  ContainerStatus = "creating"
	StatusStarting  ContainerStatus = "starting"
	StatusRunning   ContainerStatus = "running"
	StatusStopping  ContainerStatus = "stopping"
	StatusStopped   ContainerStatus = "stopped"
	StatusError     ContainerStatus = "error"
	StatusRemoving  ContainerStatus = "removing"
)

// EmulatorContainer represents a running Android emulator instance
type EmulatorContainer struct {
	ID            string            `json:"id"`              // Container ID
	Name          string            `json:"name"`            // Container name
	ImageID       string            `json:"image_id"`        // Emulator image ID
	Status        ContainerStatus   `json:"status"`          // Current status
	GRPCPort      int               `json:"grpc_port"`       // Exposed gRPC port
	ADBPort       int               `json:"adb_port"`        // Exposed ADB port
	WebRTCPort    int               `json:"webrtc_port"`     // Exposed WebRTC port (optional)
	CreatedAt     time.Time         `json:"created_at"`      // Creation timestamp
	StartedAt     *time.Time        `json:"started_at"`      // Start timestamp
	UserID        string            `json:"user_id"`         // Owner user ID
	Labels        map[string]string `json:"labels"`          // Container labels
	IPAddress     string            `json:"ip_address"`      // Internal IP address
	ErrorMessage  string            `json:"error_message,omitempty"` // Error details if any
}

// --- API Request/Response Models ---

// CreateContainerRequest represents a request to create a new emulator container
type CreateContainerRequest struct {
	ImageID     string `json:"image_id" validate:"required"` // Emulator image to use
	Name        string `json:"name"`                         // Optional custom name
	UserID      string `json:"user_id" validate:"required"`  // Owner user ID
	Memory      string `json:"memory"`                       // Memory allocation (e.g., "4g")
	CPUs        int    `json:"cpus"`                         // CPU allocation
	EnableGPU   bool   `json:"enable_gpu"`                   // Enable GPU acceleration
	EnableADB   bool   `json:"enable_adb"`                   // Expose ADB port
}

// CreateContainerResponse represents the response after creating a container
type CreateContainerResponse struct {
	Success   bool               `json:"success"`
	Container *EmulatorContainer `json:"container,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// StopContainerRequest represents a request to stop a container
type StopContainerRequest struct {
	ContainerID string `json:"container_id" validate:"required"`
	Timeout     int    `json:"timeout"` // Stop timeout in seconds (default: 10)
}

// StopContainerResponse represents the response after stopping a container
type StopContainerResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// DeleteContainerRequest represents a request to delete a container
type DeleteContainerRequest struct {
	ContainerID string `json:"container_id" validate:"required"`
	Force       bool   `json:"force"` // Force remove even if running
}

// DeleteContainerResponse represents the response after deleting a container
type DeleteContainerResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ListContainersRequest represents a request to list containers
type ListContainersRequest struct {
	UserID     string `json:"user_id"`     // Filter by user ID
	StatusFilter string `json:"status"`    // Filter by status
	Limit      int    `json:"limit"`       // Max results
	Offset     int    `json:"offset"`      // Pagination offset
}

// ListContainersResponse represents the response with container list
type ListContainersResponse struct {
	Success    bool                 `json:"success"`
	Containers []*EmulatorContainer `json:"containers"`
	Total      int                  `json:"total"`
	Error      string               `json:"error,omitempty"`
}

// GetContainerRequest represents a request to get container details
type GetContainerRequest struct {
	ContainerID string `json:"container_id" validate:"required"`
}

// GetContainerResponse represents the response with container details
type GetContainerResponse struct {
	Success   bool               `json:"success"`
	Container *EmulatorContainer `json:"container,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// ListImagesResponse represents available emulator images
type ListImagesResponse struct {
	Success bool            `json:"success"`
	Images  []*EmulatorImage `json:"images"`
	Error   string          `json:"error,omitempty"`
}

// ConnectEmulatorRequest represents a request to connect to an emulator's stream
type ConnectEmulatorRequest struct {
	ContainerID string `json:"container_id" validate:"required"`
}

// ConnectEmulatorResponse represents the emulator connection details
type ConnectEmulatorResponse struct {
	Success     bool   `json:"success"`
	GRPCAddress string `json:"grpc_address,omitempty"` // gRPC endpoint
	ADBAddress  string `json:"adb_address,omitempty"`  // ADB endpoint
	Error       string `json:"error,omitempty"`
}

// HealthResponse represents health check response
type HealthResponse struct {
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
}

// ErrorResponse represents a generic error response
type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
}


package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"android_cloud_backend/internal/config"
	"android_cloud_backend/internal/database"
	"android_cloud_backend/internal/models"
)

// Manager handles Docker container operations for Android emulators
type Manager struct {
	httpClient    *http.Client
	config        *config.Config
	database      *database.Service
	containers    map[string]*models.EmulatorContainer
	mu            sync.RWMutex
	portAllocator *PortAllocator
	dockerHost    string
}

// PortAllocator manages port allocation for containers
type PortAllocator struct {
	mu        sync.Mutex
	usedPorts map[int]bool
	minPort   int
	maxPort   int
}

// NewPortAllocator creates a new port allocator
func NewPortAllocator(minPort, maxPort int) *PortAllocator {
	return &PortAllocator{
		usedPorts: make(map[int]bool),
		minPort:   minPort,
		maxPort:   maxPort,
	}
}

// Allocate finds and reserves an available port
func (pa *PortAllocator) Allocate() (int, error) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	for port := pa.minPort; port <= pa.maxPort; port++ {
		if !pa.usedPorts[port] {
			pa.usedPorts[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports in range %d-%d", pa.minPort, pa.maxPort)
}

// Release frees a previously allocated port
func (pa *PortAllocator) Release(port int) {
	pa.mu.Lock()
	defer pa.mu.Unlock()
	delete(pa.usedPorts, port)
}

// Docker API response types
type dockerContainer struct {
	ID              string                 `json:"Id"`
	Names           []string               `json:"Names"`
	Image           string                 `json:"Image"`
	State           string                 `json:"State"`
	Status          string                 `json:"Status"`
	Created         int64                  `json:"Created"`
	Labels          map[string]string      `json:"Labels"`
	Ports           []dockerPort           `json:"Ports"`
	NetworkSettings *dockerNetworkSettings `json:"NetworkSettings"`
}

type dockerPort struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type dockerNetworkSettings struct {
	Networks map[string]dockerNetwork `json:"Networks"`
}

type dockerNetwork struct {
	IPAddress string `json:"IPAddress"`
}

type dockerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

type dockerInspectResponse struct {
	ID              string                       `json:"Id"`
	Name            string                       `json:"Name"`
	Created         string                       `json:"Created"`
	State           dockerState                  `json:"State"`
	Config          dockerConfig                 `json:"Config"`
	NetworkSettings dockerNetworkSettingsInspect `json:"NetworkSettings"`
}

type dockerState struct {
	Status    string    `json:"Status"`
	Running   bool      `json:"Running"`
	StartedAt time.Time `json:"StartedAt"`
}

type dockerConfig struct {
	Labels map[string]string `json:"Labels"`
}

type dockerNetworkSettingsInspect struct {
	Ports    map[string][]dockerPortBinding `json:"Ports"`
	Networks map[string]dockerNetwork       `json:"Networks"`
}

type dockerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// NewManager creates a new container manager
func NewManager(cfg *config.Config, db *database.Service) (*Manager, error) {
	// Create HTTP client for Docker socket communication
	// On Linux/macOS: unix:///var/run/docker.sock
	// On Windows: npipe:////./pipe/docker_engine

	dockerHost := cfg.Docker.Host
	if dockerHost == "" {
		// Auto-detect based on OS with fallback options
		if _, err := os.Stat("/var/run/docker.sock"); err == nil {
			// Linux/macOS: Unix socket
			dockerHost = "unix:///var/run/docker.sock"
		} else {
			// Windows: Try multiple connection methods
			dockerHost = "tcp://localhost:2375" // Default to TCP (needs to be enabled in Docker Desktop)
		}
	}

	var httpClient *http.Client
	var apiHost string

	if strings.HasPrefix(dockerHost, "unix://") {
		socketPath := strings.TrimPrefix(dockerHost, "unix://")
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
			Timeout: 30 * time.Second,
		}
		apiHost = "http://localhost"
	} else if strings.HasPrefix(dockerHost, "npipe://") {
		// Windows named pipe - not implemented yet
		log.Printf("⚠️  Named pipe connection not implemented, trying TCP fallback...")
		httpClient = &http.Client{Timeout: 30 * time.Second}
		apiHost = "http://localhost:2375"
	} else if strings.HasPrefix(dockerHost, "tcp://") {
		httpClient = &http.Client{Timeout: 30 * time.Second}
		apiHost = "http://" + strings.TrimPrefix(dockerHost, "tcp://")
	} else {
		httpClient = &http.Client{Timeout: 30 * time.Second}
		apiHost = dockerHost
	}

	log.Printf("🔌 Attempting to connect to Docker at: %s", dockerHost)
	log.Printf("💡 If connection fails, run: .\\enable_docker_tcp.ps1")

	manager := &Manager{
		httpClient:    httpClient,
		config:        cfg,
		database:      db,
		containers:    make(map[string]*models.EmulatorContainer),
		portAllocator: NewPortAllocator(10000, 20000),
		dockerHost:    apiHost,
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := manager.ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to Docker: %w", err)
	}

	log.Println("✅ Connected to Docker daemon")

	// Load existing containers
	if err := manager.syncContainers(context.Background()); err != nil {
		log.Printf("⚠️  Warning: Failed to sync existing containers: %v", err)
	}

	return manager, nil
}

// ping tests the Docker connection
func (m *Manager) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", m.dockerHost+"/_ping", nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker ping failed: %s", resp.Status)
	}
	return nil
}

// doRequest performs a Docker API request
func (m *Manager) doRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.dockerHost+path, bodyReader)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return m.httpClient.Do(req)
}

// syncContainers loads existing Aether containers from Docker
func (m *Manager) syncContainers(ctx context.Context) error {
	resp, err := m.doRequest(ctx, "GET", "/v1.44/containers/json?all=true&filters=%7B%22label%22%3A%5B%22app%3Daether-droid%22%5D%7D", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("list containers failed: %s - %s", resp.Status, string(body))
	}

	var containers []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range containers {
		ec := m.dockerToEmulatorContainer(&c)
		m.containers[c.ID] = ec

		// Reserve ports that are already in use by existing containers
		if ec.GRPCPort > 0 {
			m.portAllocator.mu.Lock()
			m.portAllocator.usedPorts[ec.GRPCPort] = true
			m.portAllocator.mu.Unlock()
		}
		if ec.ADBPort > 0 {
			m.portAllocator.mu.Lock()
			m.portAllocator.usedPorts[ec.ADBPort] = true
			m.portAllocator.mu.Unlock()
		}

		log.Printf("📦 Found existing container: %s (%s) - gRPC:%d, ADB:%d",
			ec.Name, ec.Status, ec.GRPCPort, ec.ADBPort)
	}

	log.Printf("📊 Synced %d existing containers", len(containers))
	return nil
}

// dockerToEmulatorContainer converts Docker container to our model
func (m *Manager) dockerToEmulatorContainer(c *dockerContainer) *models.EmulatorContainer {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}

	ec := &models.EmulatorContainer{
		ID:        c.ID,
		Name:      name,
		ImageID:   c.Labels["emulator-image-id"],
		UserID:    c.Labels["user-id"],
		Labels:    c.Labels,
		CreatedAt: time.Unix(c.Created, 0),
	}

	// Parse port mappings
	for _, port := range c.Ports {
		switch port.PrivatePort {
		case 8554:
			ec.GRPCPort = port.PublicPort
		case 5555:
			ec.ADBPort = port.PublicPort
		}
	}

	// Map Docker state to our status
	switch c.State {
	case "running":
		ec.Status = models.StatusRunning
		startedAt := time.Unix(c.Created, 0)
		ec.StartedAt = &startedAt
	case "created":
		ec.Status = models.StatusCreating
	case "paused", "restarting":
		ec.Status = models.StatusStopping
	case "exited", "dead":
		ec.Status = models.StatusStopped
	default:
		ec.Status = models.StatusError
	}

	// Get IP address
	if c.NetworkSettings != nil {
		for _, net := range c.NetworkSettings.Networks {
			if net.IPAddress != "" {
				ec.IPAddress = net.IPAddress
				break
			}
		}
	}

	return ec
}

// CreateContainer creates and starts a new Android emulator container
func (m *Manager) CreateContainer(ctx context.Context, req *models.CreateContainerRequest) (*models.EmulatorContainer, error) {
	// Get the image details
	emulatorImage := GetImageByID(m.config.Docker.Registry, req.ImageID)
	if emulatorImage == nil {
		return nil, fmt.Errorf("unknown image ID: %s", req.ImageID)
	}

	// Allocate ports
	grpcPort, err := m.portAllocator.Allocate()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate gRPC port: %w", err)
	}

	adbPort := 0
	if req.EnableADB {
		adbPort, err = m.portAllocator.Allocate()
		if err != nil {
			m.portAllocator.Release(grpcPort)
			return nil, fmt.Errorf("failed to allocate ADB port: %w", err)
		}
	}

	// Generate container name
	containerName := req.Name
	if containerName == "" {
		containerName = fmt.Sprintf("aether-emu-%s-%d", req.UserID, time.Now().Unix())
	}

	// Read ADB key if available
	adbKey := ""
	if keyData, err := os.ReadFile(m.config.Emulator.ADBKeyPath); err == nil {
		adbKey = string(keyData)
	}

	// Build environment variables
	env := []string{}
	if adbKey != "" {
		env = append(env, fmt.Sprintf("ADBKEY=%s", adbKey))
	}

	// Build port bindings
	exposedPorts := map[string]interface{}{
		"8554/tcp": struct{}{},
	}
	portBindings := map[string][]map[string]string{
		"8554/tcp": {{"HostPort": strconv.Itoa(grpcPort)}},
	}

	if req.EnableADB && adbPort > 0 {
		exposedPorts["5555/tcp"] = struct{}{}
		portBindings["5555/tcp"] = []map[string]string{{"HostPort": strconv.Itoa(adbPort)}}
	}

	// Build labels
	labels := map[string]string{
		"app":               "aether-droid",
		"managed-by":        "aether-backend",
		"user-id":           req.UserID,
		"emulator-image-id": req.ImageID,
	}

	// Create the container record
	emulatorContainer := &models.EmulatorContainer{
		Name:      containerName,
		ImageID:   req.ImageID,
		Status:    models.StatusCreating,
		GRPCPort:  grpcPort,
		ADBPort:   adbPort,
		CreatedAt: time.Now(),
		UserID:    req.UserID,
		Labels:    labels,
	}

	// Pull image if needed
	log.Printf("📥 Checking image: %s", emulatorImage.FullImage)
	if err := m.pullImage(ctx, emulatorImage.FullImage); err != nil {
		log.Printf("⚠️  Image pull warning: %v (may already exist)", err)
	}

	// Build host config
	devices := []map[string]string{}

	// KVM device for hardware acceleration
	devices = append(devices, map[string]string{
		"PathOnHost":        "/dev/kvm",
		"PathInContainer":   "/dev/kvm",
		"CgroupPermissions": "rwm",
	})

	// Parse memory
	memory := req.Memory
	if memory == "" {
		memory = m.config.Emulator.DefaultMemory
	}
	var memoryBytes int64
	if strings.HasSuffix(memory, "g") || strings.HasSuffix(memory, "G") {
		val, _ := strconv.ParseInt(memory[:len(memory)-1], 10, 64)
		memoryBytes = val * 1024 * 1024 * 1024
	} else if strings.HasSuffix(memory, "m") || strings.HasSuffix(memory, "M") {
		val, _ := strconv.ParseInt(memory[:len(memory)-1], 10, 64)
		memoryBytes = val * 1024 * 1024
	}

	// Create container request body
	createBody := map[string]interface{}{
		"Image":        emulatorImage.FullImage,
		"Env":          env,
		"ExposedPorts": exposedPorts,
		"Labels":       labels,
		"HostConfig": map[string]interface{}{
			"PortBindings": portBindings,
			"Devices":      devices,
			"Memory":       memoryBytes,
			"Tmpfs": map[string]string{
				"/data": "size=2g",
			},
		},
	}

	log.Printf("🚀 Creating container: %s (image: %s)", containerName, emulatorImage.FullImage)

	// Create container
	resp, err := m.doRequest(ctx, "POST", "/v1.44/containers/create?name="+containerName, createBody)
	if err != nil {
		m.portAllocator.Release(grpcPort)
		if adbPort > 0 {
			m.portAllocator.Release(adbPort)
		}
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		m.portAllocator.Release(grpcPort)
		if adbPort > 0 {
			m.portAllocator.Release(adbPort)
		}
		return nil, fmt.Errorf("failed to create container: %s - %s", resp.Status, string(body))
	}

	var createResp dockerCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}

	emulatorContainer.ID = createResp.ID

	// Start the container
	emulatorContainer.Status = models.StatusStarting
	log.Printf("▶️  Starting container: %s", createResp.ID[:12])

	startResp, err := m.doRequest(ctx, "POST", fmt.Sprintf("/v1.44/containers/%s/start", createResp.ID), nil)
	if err != nil {
		m.deleteContainer(ctx, createResp.ID, true)
		m.portAllocator.Release(grpcPort)
		if adbPort > 0 {
			m.portAllocator.Release(adbPort)
		}
		return nil, fmt.Errorf("failed to start container: %w", err)
	}
	startResp.Body.Close()

	if startResp.StatusCode != http.StatusNoContent && startResp.StatusCode != http.StatusOK {
		m.deleteContainer(ctx, createResp.ID, true)
		m.portAllocator.Release(grpcPort)
		if adbPort > 0 {
			m.portAllocator.Release(adbPort)
		}
		return nil, fmt.Errorf("failed to start container: %s", startResp.Status)
	}

	// Update status
	startTime := time.Now()
	emulatorContainer.Status = models.StatusRunning
	emulatorContainer.StartedAt = &startTime

	// Ensure all required fields are set
	if emulatorContainer.CreatedAt.IsZero() {
		emulatorContainer.CreatedAt = startTime
	}

	// Get container IP
	if inspectResp, err := m.inspectContainer(ctx, createResp.ID); err == nil {
		if inspectResp.NetworkSettings.Networks != nil {
			for _, net := range inspectResp.NetworkSettings.Networks {
				if net.IPAddress != "" {
					emulatorContainer.IPAddress = net.IPAddress
					break
				}
			}
		}
	}

	// Save to database (if available)
	if m.database != nil {
		if err := m.database.CreateContainer(ctx, emulatorContainer); err != nil {
			log.Printf("⚠️  Failed to save container to database: %v", err)
			// Don't fail the creation, just log the error
		}
	} else {
		log.Printf("💾 Database not available, skipping container persistence")
	}

	// Store in our map
	m.mu.Lock()
	m.containers[createResp.ID] = emulatorContainer
	m.mu.Unlock()

	log.Printf("✅ Container started: %s (gRPC: %d, ADB: %d)", containerName, grpcPort, adbPort)

	return emulatorContainer, nil
}

// pullImage pulls a Docker image
func (m *Manager) pullImage(ctx context.Context, imageName string) error {
	resp, err := m.doRequest(ctx, "POST", "/v1.44/images/create?fromImage="+imageName, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Consume the output
	io.Copy(io.Discard, resp.Body)
	return nil
}

// inspectContainer gets detailed container information
func (m *Manager) inspectContainer(ctx context.Context, containerID string) (*dockerInspectResponse, error) {
	resp, err := m.doRequest(ctx, "GET", fmt.Sprintf("/v1.44/containers/%s/json", containerID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("inspect failed: %s - %s", resp.Status, string(body))
	}

	var inspect dockerInspectResponse
	if err := json.NewDecoder(resp.Body).Decode(&inspect); err != nil {
		return nil, err
	}

	return &inspect, nil
}

// StopContainer stops a running container
func (m *Manager) StopContainer(ctx context.Context, containerID string, timeout int) error {
	m.mu.Lock()
	if ec, exists := m.containers[containerID]; exists {
		ec.Status = models.StatusStopping
	}
	m.mu.Unlock()

	if timeout == 0 {
		timeout = 10
	}

	log.Printf("⏹️  Stopping container: %s", containerID[:12])

	resp, err := m.doRequest(ctx, "POST", fmt.Sprintf("/v1.44/containers/%s/stop?t=%d", containerID, timeout), nil)
	if err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("failed to stop container: %s", resp.Status)
	}

	m.mu.Lock()
	if ec, exists := m.containers[containerID]; exists {
		ec.Status = models.StatusStopped
	}
	m.mu.Unlock()

	// Update database (if available)
	if m.database != nil {
		if err := m.database.UpdateContainerStatus(ctx, containerID, models.StatusStopped, nil); err != nil {
			log.Printf("⚠️  Failed to update container status in database: %v", err)
		}
	}

	log.Printf("✅ Container stopped: %s", containerID[:12])
	return nil
}

// deleteContainer removes a container (internal use)
func (m *Manager) deleteContainer(ctx context.Context, containerID string, force bool) error {
	forceParam := ""
	if force {
		forceParam = "&force=true"
	}

	resp, err := m.doRequest(ctx, "DELETE", fmt.Sprintf("/v1.44/containers/%s?v=true%s", containerID, forceParam), nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DeleteContainer removes a container (public API)
func (m *Manager) DeleteContainer(ctx context.Context, containerID string, force bool) error {
	m.mu.Lock()
	ec, exists := m.containers[containerID]
	if exists {
		ec.Status = models.StatusRemoving
	}
	m.mu.Unlock()

	log.Printf("🗑️  Removing container: %s (force: %v)", containerID[:12], force)

	// Release ports
	m.mu.RLock()
	if ec, exists := m.containers[containerID]; exists {
		if ec.GRPCPort > 0 {
			m.portAllocator.Release(ec.GRPCPort)
		}
		if ec.ADBPort > 0 {
			m.portAllocator.Release(ec.ADBPort)
		}
	}
	m.mu.RUnlock()

	if err := m.deleteContainer(ctx, containerID, force); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	m.mu.Lock()
	delete(m.containers, containerID)
	m.mu.Unlock()

	// Remove from database (if available)
	if m.database != nil {
		if err := m.database.DeleteContainer(ctx, containerID); err != nil {
			log.Printf("⚠️  Failed to remove container from database: %v", err)
		}
	}

	log.Printf("✅ Container removed: %s", containerID[:12])
	return nil
}

// GetContainer returns container details
func (m *Manager) GetContainer(ctx context.Context, containerID string) (*models.EmulatorContainer, error) {
	// First check our in-memory cache
	m.mu.RLock()
	ec, exists := m.containers[containerID]
	m.mu.RUnlock()

	if exists {
		return ec, nil
	}

	// Try to get from database (if available)
	if m.database != nil {
		ec, err := m.database.GetContainer(ctx, containerID)
		if err != nil {
			return nil, fmt.Errorf("container not found: %s", containerID)
		}
		return ec, nil
	}

	return nil, fmt.Errorf("container not found: %s", containerID)
}

// ListContainers returns all containers matching the filter
func (m *Manager) ListContainers(ctx context.Context, userID string, statusFilter string) ([]*models.EmulatorContainer, error) {
	var containers []*models.EmulatorContainer

	// Get containers from database (if available)
	if m.database != nil {
		var err error
		containers, err = m.database.GetUserContainers(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to get containers from database: %w", err)
		}
	} else {
		// Return in-memory containers only
		m.mu.RLock()
		for _, c := range m.containers {
			if c.UserID == userID {
				containers = append(containers, c)
			}
		}
		m.mu.RUnlock()
	}

	// Apply status filter
	if statusFilter != "" {
		filtered := make([]*models.EmulatorContainer, 0)
		for _, c := range containers {
			if string(c.Status) == statusFilter {
				filtered = append(filtered, c)
			}
		}
		containers = filtered
	}

	return containers, nil
}

// GetContainerAddress returns the gRPC address for connecting to an emulator
func (m *Manager) GetContainerAddress(ctx context.Context, containerID string) (string, error) {
	ec, err := m.GetContainer(ctx, containerID)
	if err != nil {
		return "", err
	}

	if ec.Status != models.StatusRunning {
		return "", fmt.Errorf("container is not running: %s", ec.Status)
	}

	return fmt.Sprintf("localhost:%d", ec.GRPCPort), nil
}

// Close cleans up the manager
func (m *Manager) Close() error {
	return nil
}

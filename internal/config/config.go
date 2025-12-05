package config

import (
	"os"
	"runtime"
	"strconv"
)

// Config holds all application configuration
type Config struct {
	Server   ServerConfig
	Docker   DockerConfig
	Emulator EmulatorConfig
	WebRTC   WebRTCConfig
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port           string
	AllowedOrigins []string
	ReadTimeout    int // seconds
	WriteTimeout   int // seconds
}

// DockerConfig holds Docker-related configuration
type DockerConfig struct {
	Host        string            // Docker host (e.g., "unix:///var/run/docker.sock")
	Registry    string            // Container registry for Android emulator images
	NetworkName string            // Docker network name for emulator containers
	Labels      map[string]string // Labels to add to containers
}

// EmulatorConfig holds Android emulator configuration
type EmulatorConfig struct {
	DefaultGRPCPort int    // Default gRPC port (8554)
	DefaultADBPort  int    // Default ADB port (5555)
	ADBKeyPath      string // Path to ADB key file
	DefaultMemory   string // Default memory allocation
	DefaultCPUs     int    // Default CPU allocation
}

// WebRTCConfig holds WebRTC configuration
type WebRTCConfig struct {
	MinPort int // Minimum UDP port for WebRTC
	MaxPort int // Maximum UDP port for WebRTC
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	// Set platform-specific Docker host defaults
	defaultDockerHost := "unix:///var/run/docker.sock" // Linux/macOS default
	if runtime.GOOS == "windows" {
		defaultDockerHost = "tcp://localhost:2375" // Windows default (requires Docker Desktop TCP access)
	}

	return &Config{
		Server: ServerConfig{
			Port:           getEnv("SERVER_PORT", "8080"),
			AllowedOrigins: []string{"*"},
			ReadTimeout:    30,
			WriteTimeout:   30,
		},
		Docker: DockerConfig{
			Host:        getEnv("DOCKER_HOST", defaultDockerHost),
			Registry:    getEnv("EMULATOR_REGISTRY", "us-docker.pkg.dev/android-emulator-268719/images"),
			NetworkName: getEnv("DOCKER_NETWORK", "aether-network"),
			Labels: map[string]string{
				"app":        "aether-droid",
				"managed-by": "aether-backend",
			},
		},
		Emulator: EmulatorConfig{
			DefaultGRPCPort: getEnvInt("EMULATOR_GRPC_PORT", 8554),
			DefaultADBPort:  getEnvInt("EMULATOR_ADB_PORT", 5555),
			ADBKeyPath:      getEnv("ADB_KEY_PATH", expandHome("~/.android/adbkey")),
			DefaultMemory:   getEnv("EMULATOR_MEMORY", "8g"),
			DefaultCPUs:     getEnvInt("EMULATOR_CPUS", 3),
		},
		WebRTC: WebRTCConfig{
			MinPort: getEnvInt("WEBRTC_MIN_PORT", 50000),
			MaxPort: getEnvInt("WEBRTC_MAX_PORT", 60000),
		},
	}
}

// Load returns the application configuration
func Load() *Config {
	return DefaultConfig()
}

// Helper functions

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return fallback
}

func expandHome(path string) string {
	if len(path) > 1 && path[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

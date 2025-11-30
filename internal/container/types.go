package container

import (
	"android_cloud_backend/internal/models"
)

// PortMapping represents a port mapping for the container
type PortMapping struct {
	ContainerPort int
	HostPort      int
	Protocol      string // "tcp" or "udp"
}

// ContainerConfig holds configuration for creating a new emulator container
type ContainerConfig struct {
	Image       string            // Docker image to use
	Name        string            // Container name
	UserID      string            // Owner user ID
	Memory      string            // Memory limit (e.g., "4g")
	CPUs        int64             // CPU limit (millicores)
	EnableGPU   bool              // Enable NVIDIA GPU
	EnableKVM   bool              // Enable KVM device
	Ports       []PortMapping     // Port mappings
	Environment map[string]string // Environment variables
	Labels      map[string]string // Container labels
	ADBKey      string            // ADB key content
	ADBKeyPub   string            // ADB public key content
}

// AvailableImages returns the list of available Android emulator images
func AvailableImages(registry string) []*models.EmulatorImage {
	// Using Google Android emulator container scripts images
	return []*models.EmulatorImage{
		// Android 11 (API 30)
		{
			ID:         "30-google-x64",
			Name:       "Android 11 (Google APIs)",
			APILevel:   30,
			AndroidVer: "11",
			Variant:    "google",
			ABI:        "x86_64",
			Tag:        "30.1.2",
			FullImage:  registry + "/30-google-x64:30.1.2",
		},
		// Android 10 (API 29)
		{
			ID:         "29-google-x64",
			Name:       "Android 10 (Google APIs)",
			APILevel:   29,
			AndroidVer: "10",
			Variant:    "google",
			ABI:        "x86_64",
			Tag:        "30.1.2",
			FullImage:  registry + "/29-google-x64:30.1.2",
		},
		// Android 9 (API 28)
		{
			ID:         "28-google-x64",
			Name:       "Android 9 Pie (Google APIs)",
			APILevel:   28,
			AndroidVer: "9",
			Variant:    "google",
			ABI:        "x86_64",
			Tag:        "30.1.2",
			FullImage:  registry + "/28-google-x64:30.1.2",
		},
	}
}

// GetImageByID finds an image by its ID
func GetImageByID(registry, imageID string) *models.EmulatorImage {
	images := AvailableImages(registry)
	for _, img := range images {
		if img.ID == imageID {
			return img
		}
	}
	return nil
}

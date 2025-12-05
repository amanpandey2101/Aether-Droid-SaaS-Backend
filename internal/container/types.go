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
// Images are from: us-docker.pkg.dev/android-emulator-268719/images/
//
// NOTE: Images must be pulled before use:
//   docker pull us-docker.pkg.dev/android-emulator-268719/images/30-google-x64:30.1.2
//
// To add more images, pull them first then add to this list.
func AvailableImages(registry string) []*models.EmulatorImage {
	return []*models.EmulatorImage{
		// ============ Android 11 (API 30) ============
		{
			ID:         "30-google-x64",
			Name:       "Android 11 (Google APIs) ⭐",
			APILevel:   30,
			AndroidVer: "11",
			Variant:    "google",
			ABI:        "x86_64",
			Tag:        "30.1.2",
			FullImage:  registry + "/30-google-x64:30.1.2",
		},
		// ============ Android 10 (API 29) ============
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
		// Add more images after pulling them
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

package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"android_cloud_backend/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type Service struct {
	client   *mongo.Client
	database *mongo.Database
}

// ContainerDocument represents the container document in MongoDB
type ContainerDocument struct {
	ID          string     `bson:"_id,omitempty"`
	UserID      string     `bson:"user_id"`
	ContainerID string     `bson:"container_id"`
	ImageID     string     `bson:"image_id"`
	Name        string     `bson:"name"`
	Status      string     `bson:"status"`
	GRPCPort    int        `bson:"grpc_port,omitempty"`
	ADBPort     int        `bson:"adb_port,omitempty"`
	WebRTCPort  int        `bson:"webrtc_port,omitempty"`
	IPAddress   string     `bson:"ip_address,omitempty"`
	CreatedAt   time.Time  `bson:"created_at"`
	StartedAt   *time.Time `bson:"started_at,omitempty"`
}

func NewService() (*Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoURL := os.Getenv("DATABASE_URL")
	// if mongoURL == "" {
	// 	return nil, fmt.Errorf("DATABASE_URL environment variable not set")
	// }

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURL))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Ping the database
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	database := client.Database("aether") // Use the same database as Next.js

	log.Println("✅ Connected to MongoDB")
	return &Service{
		client:   client,
		database: database,
	}, nil
}

// CreateContainer saves a new container to the database
func (s *Service) CreateContainer(ctx context.Context, container *models.EmulatorContainer) error {
	collection := s.database.Collection("emulators")

	doc := ContainerDocument{
		UserID:      container.UserID,
		ContainerID: container.ID,
		ImageID:     container.ImageID,
		Name:        container.Name,
		Status:      string(container.Status),
		GRPCPort:    container.GRPCPort,
		ADBPort:     container.ADBPort,
		WebRTCPort:  container.WebRTCPort,
		IPAddress:   container.IPAddress,
		CreatedAt:   container.CreatedAt,
		StartedAt:   container.StartedAt,
	}

	_, err := collection.InsertOne(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to insert container: %w", err)
	}

	log.Printf("💾 Saved container %s for user %s", container.ID[:12], container.UserID)
	return nil
}

// UpdateContainerStatus updates the status of an existing container
func (s *Service) UpdateContainerStatus(ctx context.Context, containerID string, status models.ContainerStatus, startedAt *time.Time) error {
	collection := s.database.Collection("emulators")

	filter := bson.M{"container_id": containerID}
	update := bson.M{
		"$set": bson.M{
			"status": string(status),
		},
	}

	if startedAt != nil {
		update["$set"].(bson.M)["started_at"] = startedAt
	}

	_, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update container status: %w", err)
	}

	log.Printf("📝 Updated container %s status to %s", containerID[:12], status)
	return nil
}

// GetUserContainers retrieves all containers for a user
func (s *Service) GetUserContainers(ctx context.Context, userID string) ([]*models.EmulatorContainer, error) {
	collection := s.database.Collection("emulators")

	cursor, err := collection.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, fmt.Errorf("failed to query containers: %w", err)
	}
	defer cursor.Close(ctx)

	var containers []*models.EmulatorContainer
	for cursor.Next(ctx) {
		var doc ContainerDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("failed to decode container: %w", err)
		}

		container := &models.EmulatorContainer{
			ID:         doc.ContainerID,
			Name:       doc.Name,
			ImageID:    doc.ImageID,
			UserID:     doc.UserID,
			Status:     models.ContainerStatus(doc.Status),
			GRPCPort:   doc.GRPCPort,
			ADBPort:    doc.ADBPort,
			WebRTCPort: doc.WebRTCPort,
			IPAddress:  doc.IPAddress,
			CreatedAt:  doc.CreatedAt,
			StartedAt:  doc.StartedAt,
		}
		containers = append(containers, container)
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error: %w", err)
	}

	return containers, nil
}

// GetContainer retrieves a specific container by ID
func (s *Service) GetContainer(ctx context.Context, containerID string) (*models.EmulatorContainer, error) {
	collection := s.database.Collection("emulators")

	var doc ContainerDocument
	err := collection.FindOne(ctx, bson.M{"container_id": containerID}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, fmt.Errorf("container not found")
		}
		return nil, fmt.Errorf("failed to find container: %w", err)
	}

	container := &models.EmulatorContainer{
		ID:         doc.ContainerID,
		Name:       doc.Name,
		ImageID:    doc.ImageID,
		UserID:     doc.UserID,
		Status:     models.ContainerStatus(doc.Status),
		GRPCPort:   doc.GRPCPort,
		ADBPort:    doc.ADBPort,
		WebRTCPort: doc.WebRTCPort,
		IPAddress:  doc.IPAddress,
		CreatedAt:  doc.CreatedAt,
		StartedAt:  doc.StartedAt,
	}

	return container, nil
}

// DeleteContainer removes a container from the database
func (s *Service) DeleteContainer(ctx context.Context, containerID string) error {
	collection := s.database.Collection("emulators")

	_, err := collection.DeleteOne(ctx, bson.M{"container_id": containerID})
	if err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}

	log.Printf("🗑️ Removed container %s from database", containerID[:12])
	return nil
}

// Close closes the database connection
func (s *Service) Close(ctx context.Context) error {
	return s.client.Disconnect(ctx)
}

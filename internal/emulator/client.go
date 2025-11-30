package emulator

import (
	pb "android_cloud_backend/internal/emulator/proto"
	"context"
	"sync"

	"google.golang.org/grpc"
)

type EmulatorClient struct {
	conn     *grpc.ClientConn
	client   pb.EmulatorControllerClient
	stream   pb.EmulatorController_StreamScreenshotClient
	streamMu sync.Mutex
}

func NewEmulatorClient(addr string) (*EmulatorClient, error) {
	conn, err := grpc.Dial(
		addr,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(2000*1024*1024), // 2GB for large frames (should handle any emulator output)
			grpc.MaxCallSendMsgSize(2000*1024*1024),
		),
	)

	if err != nil {
		return nil, err
	}

	return &EmulatorClient{
		conn:   conn,
		client: pb.NewEmulatorControllerClient(conn),
	}, nil
}

// SendKey sends a keyboard event to the emulator
func (ec *EmulatorClient) SendKey(keyEvent *pb.KeyboardEvent) error {
	_, err := ec.client.SendKey(context.Background(), keyEvent)
	return err
}

// SendTouch sends a touch event to the emulator
func (ec *EmulatorClient) SendTouch(touchEvent *pb.TouchEvent) error {
	_, err := ec.client.SendTouch(context.Background(), touchEvent)
	return err
}

// Close closes the gRPC connection and cleans up resources
func (ec *EmulatorClient) Close() error {
	ec.closeStream()
	if ec.conn != nil {
		return ec.conn.Close()
	}
	return nil
}

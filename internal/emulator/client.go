package emulator

import (
    "google.golang.org/grpc"
    pb "android_cloud_backend/internal/emulator/proto"
)

type EmulatorClient struct {
    conn   *grpc.ClientConn
    client pb.EmulatorControllerClient
}

func NewEmulatorClient(addr string) (*EmulatorClient, error) {
	conn, err := grpc.Dial(
		addr,
		grpc.WithInsecure(),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(20*1024*1024), // 20 MB
			grpc.MaxCallSendMsgSize(20*1024*1024),
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

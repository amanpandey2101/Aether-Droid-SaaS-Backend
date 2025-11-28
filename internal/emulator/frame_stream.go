package emulator

import (
    "context"
    "image"

    "log"

    pb "android_cloud_backend/internal/emulator/proto"
)

func (e *EmulatorClient) GetFrame() (image.Image, error) {
    log.Println("🎞 Requesting frame from StreamScreenshot...")

    // Open streaming RPC
    stream, err := e.client.StreamScreenshot(context.Background(), &pb.ImageFormat{
        Format: pb.ImageFormat_RGBA8888,
    })
    if err != nil {
        log.Println("❌ StreamScreenshot error:", err)
        return nil, err
    }

    // Receive ONE frame
    res, err := stream.Recv()
    if err != nil {
        log.Println("❌ StreamScreenshot recv error:", err)
        return nil, err
    }

    // Logs for debugging
    log.Println("📥 StreamScreenshot response:")
    log.Println("    res.Format.Width:", res.Format.Width)
    log.Println("    res.Format.Height:", res.Format.Height)
    log.Println("    len(res.Image):", len(res.Image))

    w := int(res.Format.Width)
    h := int(res.Format.Height)

    if w == 0 || h == 0 {
        log.Println("❌ Invalid screenshot resolution:", w, "x", h)
        return nil, nil
    }

    // Create output RGBA image
    img := image.NewRGBA(image.Rect(0, 0, w, h))
    copy(img.Pix, res.Image)

    log.Println("✅ Frame successfully decoded into RGBA")

    return img, nil
}

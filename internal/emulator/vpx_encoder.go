// internal/emulator/vpx_encoder.go
package emulator

/*
#cgo pkg-config: vpx
#include "vpx_encoder.h"
#include <stdlib.h>
*/
import "C"
import (
    "errors"
    "image"
    "unsafe"
)

type VPXEncoder struct {
    cenc *C.VPXEncoder
    width int
    height int
}

// NewVPXEncoder creates encoder. bitrate in bits/sec (e.g., 800_000)
func NewVPXEncoder(width, height, bitrate int) (*VPXEncoder, error) {
    c := C.vpx_encoder_new(C.int(width), C.int(height), C.int(bitrate))
    if c == nil {
        return nil, errors.New("vpx: failed to create encoder (libvpx init)")
    }
    return &VPXEncoder{cenc: c, width: width, height: height}, nil
}

// EncodeImage converts RGBA image.Image -> I420 planes and calls libvpx encoder.
// Returns a byte slice (ownership transferred to Go, must be freed) or error.
func (v *VPXEncoder) EncodeImage(img image.Image, ptsUs uint64) ([]byte, error) {
    // Ensure image has correct bounds
    bounds := img.Bounds()
    w := bounds.Dx()
    h := bounds.Dy()
    if w != v.width || h != v.height {
        // If sizes differ, we could rescale but for now return error
        return nil, errors.New("vpx: image size mismatch")
    }

    // Convert RGBA -> YUV420 (I420)
    yStride := w
    uvStride := (w + 1) / 2
    ySize := yStride * h
    uvH := (h + 1) / 2
    uvSize := uvStride * uvH

    y := make([]byte, ySize)
    u := make([]byte, uvSize)
    vplane := make([]byte, uvSize)

    // Convert pixel by pixel
    // img.At uses color.Color; for performance, attempt type assertion to *image.RGBA
    if rgba, ok := img.(*image.RGBA); ok {
        // Fast path: use rgba.Pix
        // RGBA Pix is R,G,B,A per pixel
        // However emulator provided pixel order may be RGBA already in img
        // We'll convert from RGBA -> I420
        for j := 0; j < h; j++ {
            for i := 0; i < w; i++ {
                off := j*rgba.Stride + i*4
                r := rgba.Pix[off]
                g := rgba.Pix[off+1]
                b := rgba.Pix[off+2]
                // convert to YUV (BT.601)
                yv := ( (  66*int(r) + 129*int(g) +  25*int(b) + 128) >> 8) + 16
                uv := ( ( -38*int(r) -  74*int(g) + 112*int(b) + 128) >> 8) + 128
                vv := ( ( 112*int(r) -  94*int(g) -  18*int(b) + 128) >> 8) + 128
                if yv < 0 { yv = 0 } else if yv > 255 { yv = 255 }
                if uv < 0 { uv = 0 } else if uv > 255 { uv = 255 }
                if vv < 0 { vv = 0 } else if vv > 255 { vv = 255 }
                y[j*yStride+i] = byte(yv)
                // subsample u/v (4:2:0) - take top-left of each 2x2 block
                if (j%2)==0 && (i%2)==0 {
                    ui := (j/2)*uvStride + (i/2)
                    u[ui] = byte(uv)
                    vplane[ui] = byte(vv)
                }
            }
        }
    } else {
        // Generic path (slower)
        for j := 0; j < h; j++ {
            for i := 0; i < w; i++ {
                r32, g32, b32, _ := img.At(bounds.Min.X+i, bounds.Min.Y+j).RGBA()
                r := uint8(r32 >> 8)
                g := uint8(g32 >> 8)
                b := uint8(b32 >> 8)
                yv := ( (  66*int(r) + 129*int(g) +  25*int(b) + 128) >> 8) + 16
                uv := ( ( -38*int(r) -  74*int(g) + 112*int(b) + 128) >> 8) + 128
                vv := ( ( 112*int(r) -  94*int(g) -  18*int(b) + 128) >> 8) + 128
                if yv < 0 { yv = 0 } else if yv > 255 { yv = 255 }
                if uv < 0 { uv = 0 } else if uv > 255 { uv = 255 }
                if vv < 0 { vv = 0 } else if vv > 255 { vv = 255 }
                y[j*yStride+i] = byte(yv)
                if (j%2)==0 && (i%2)==0 {
                    ui := (j/2)*uvStride + (i/2)
                    u[ui] = byte(uv)
                    vplane[ui] = byte(vv)
                }
            }
        }
    }

    // Call C encoder
    var outLen C.int
    // Convert Go slices to C pointers
    cy := (*C.uint8_t)(unsafe.Pointer(&y[0]))
    cu := (*C.uint8_t)(unsafe.Pointer(&u[0]))
    cv := (*C.uint8_t)(unsafe.Pointer(&vplane[0]))

    cbuf := C.vpx_encoder_encode(
        v.cenc,
        cy, cu, cv,
        C.int(yStride),
        C.int(uvStride),
        C.uint64_t(ptsUs),
        &outLen,
    )
    
    if cbuf == nil || outLen == 0 {
        return nil, errors.New("vpx: encode failed or returned empty")
    }
    // Copy C buffer into Go slice, then free C buffer
    out := C.GoBytes(unsafe.Pointer(cbuf), outLen)
    C.free(unsafe.Pointer(cbuf))
    return out, nil
}

func (v *VPXEncoder) Close() {
    if v.cenc != nil {
        C.vpx_encoder_free(v.cenc)
        v.cenc = nil
    }
}

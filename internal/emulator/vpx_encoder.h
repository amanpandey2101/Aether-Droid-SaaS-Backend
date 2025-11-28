// internal/emulator/vpx_encoder.h
#ifndef VPX_ENCODER_H
#define VPX_ENCODER_H

#include <stdint.h>

typedef struct VPXEncoder VPXEncoder;

// Initialize encoder. Returns pointer or NULL on failure.
VPXEncoder* vpx_encoder_new(int width, int height, int bitrate);

// Encode a single frame in I420 format.
// pts is presentation timestamp in microseconds.
// on success, returns pointer to buffer and sets out_len; caller must free returned buffer with free()
// on failure returns NULL and out_len = 0
uint8_t* vpx_encoder_encode(
    VPXEncoder* enc,
    uint8_t* y_plane,
    uint8_t* u_plane,
    uint8_t* v_plane,
    int y_stride,
    int uv_stride,
    uint64_t pts,   // this is uint64_t
    int* out_len
);

// Free encoder
void vpx_encoder_free(VPXEncoder* enc);

#endif // VPX_ENCODER_H

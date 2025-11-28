// internal/emulator/vpx_encoder.c
#include "vpx_encoder.h"
#include <stdlib.h>
#include <string.h>
#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>

struct VPXEncoder {
    vpx_codec_ctx_t codec;
    vpx_image_t raw;
    int width;
    int height;
};

VPXEncoder* vpx_encoder_new(int width, int height, int bitrate) {
    VPXEncoder* e = (VPXEncoder*)malloc(sizeof(VPXEncoder));
    if (!e) return NULL;
    memset(e, 0, sizeof(VPXEncoder));
    e->width = width;
    e->height = height;

    vpx_codec_err_t res;
    vpx_codec_iface_t* iface = vpx_codec_vp8_cx();

    vpx_codec_enc_cfg_t cfg;
    res = vpx_codec_enc_config_default(iface, &cfg, 0);
    if (res) {
        free(e);
        return NULL;
    }

    cfg.g_w = width;
    cfg.g_h = height;
    cfg.rc_target_bitrate = bitrate / 1000; // kbps

    if (vpx_img_alloc(&e->raw, VPX_IMG_FMT_I420, width, height, 1) == NULL) {
        free(e);
        return NULL;
    }

    if (vpx_codec_enc_init(&e->codec, iface, &cfg, 0)) {
        vpx_img_free(&e->raw);
        free(e);
        return NULL;
    }

    return e;
}

uint8_t* vpx_encoder_encode(VPXEncoder* enc, uint8_t* y_plane, uint8_t* u_plane, uint8_t* v_plane, int y_stride, int uv_stride, uint64_t pts, int* out_len) {
    if (!enc) { *out_len = 0; return NULL; }

    // Copy planes into vpx_image_t
    int h = enc->height;
    for (int row = 0; row < h; row++) {
        memcpy(enc->raw.planes[0] + row * enc->raw.stride[0], y_plane + row * y_stride, enc->width);
    }
    int chroma_h = (h + 1) / 2;
    int chroma_w = (enc->width + 1) / 2;
    for (int row = 0; row < chroma_h; row++) {
        memcpy(enc->raw.planes[1] + row * enc->raw.stride[1], u_plane + row * uv_stride, chroma_w);
        memcpy(enc->raw.planes[2] + row * enc->raw.stride[2], v_plane + row * uv_stride, chroma_w);
    }

    if (vpx_codec_encode(&enc->codec, &enc->raw, pts, 1, 0, VPX_DL_REALTIME)) {
        *out_len = 0;
        return NULL;
    }

    vpx_codec_iter_t iter = NULL;
    const vpx_codec_cx_pkt_t* pkt;
    // Collect encoded bytes into a buffer (single frame might have multiple packets; concatenate)
    uint8_t* outbuf = NULL;
    int total = 0;
    while ((pkt = vpx_codec_get_cx_data(&enc->codec, &iter))) {
        if (pkt->kind == VPX_CODEC_CX_FRAME_PKT) {
            const uint8_t* data = (const uint8_t*)pkt->data.frame.buf;
            int sz = (int)pkt->data.frame.sz;
            uint8_t* tmp = (uint8_t*)realloc(outbuf, total + sz);
            if (!tmp) { free(outbuf); *out_len = 0; return NULL; }
            outbuf = tmp;
            memcpy(outbuf + total, data, sz);
            total += sz;
        }
    }
    *out_len = total;
    return outbuf;
}

void vpx_encoder_free(VPXEncoder* enc) {
    if (!enc) return;
    vpx_codec_destroy(&enc->codec);
    vpx_img_free(&enc->raw);
    free(enc);
}

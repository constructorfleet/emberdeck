#ifndef EMBERDECK_SHA256_INTERNAL_H
#define EMBERDECK_SHA256_INTERNAL_H
#include <stddef.h>
#include <stdint.h>
typedef struct {
    uint32_t h[8];
    uint64_t bits;
    unsigned char block[64];
    size_t used;
} ed_sha256_context;
void ed_sha256_init_internal(ed_sha256_context *context);
void ed_sha256_update_internal(ed_sha256_context *context,
                               const unsigned char *bytes, size_t length);
void ed_sha256_finish_internal(ed_sha256_context *context,
                               unsigned char digest[32]);
#endif

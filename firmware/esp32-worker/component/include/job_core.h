#ifndef EMBERDECK_JOB_CORE_H
#define EMBERDECK_JOB_CORE_H

#include <stddef.h>
#include <stdint.h>

#define ED_MAX_PROGRAM_BYTES (4u * 1024u * 1024u)
#define ED_MAX_RANGE_BYTES (64u * 1024u)

typedef enum {
    ED_OK = 0,
    ED_ERR_ARGUMENT,
    ED_ERR_TOO_LARGE,
    ED_ERR_FETCH,
    ED_ERR_STORAGE,
    ED_ERR_SIZE,
    ED_ERR_DIGEST,
    ED_ERR_GCODE,
    ED_ERR_MEMORY,
    ED_ERR_UART
} ed_result;

typedef struct {
    size_t size;
    unsigned char sha256[32];
} ed_manifest;

typedef struct {
    void *context;
    int (*reset)(void *context, size_t expected_size);
    int (*write)(void *context, size_t offset, const unsigned char *bytes,
                 size_t length);
    int (*read)(void *context, size_t offset, unsigned char *bytes,
                size_t length);
    /* seal prevents any writer from changing the staged generation until
     * unseal is called by ed_verified_release. */
    int (*seal)(void *context, size_t expected_size);
    void (*unseal)(void *context);
} ed_storage;

typedef int (*ed_range_fetch)(void *context, size_t offset, size_t max_length,
                              unsigned char *destination, size_t capacity,
                              size_t *received);
typedef int (*ed_uart_sink)(void *context, const unsigned char *bytes,
                            size_t length);

typedef struct ed_verified_job ed_verified_job;

/* Digest is the raw 32-byte SHA-256 value. Fetch is called sequentially from
 * offset zero, with each requested range no larger than ED_MAX_RANGE_BYTES.
 * A short range is treated as truncation. Storage is reset before staging. */
ed_result ed_stage_verify(const ed_manifest *manifest, ed_range_fetch fetch,
                          void *fetch_context, const ed_storage *storage,
                          ed_verified_job **verified_out);

/* Only a successful ed_stage_verify call can construct this opaque handle.
 * The storage backend remains sealed until release. Sending consumes the
 * handle even if the sink reports an error; a verified job is never replayed. */
ed_result ed_verified_send_uart(ed_verified_job *verified,
                                ed_uart_sink sink, void *sink_context);
void ed_verified_release(ed_verified_job *verified);

void ed_sha256(const unsigned char *bytes, size_t length,
               unsigned char digest_out[32]);

#endif

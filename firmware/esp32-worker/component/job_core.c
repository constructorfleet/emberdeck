#include "job_core.h"
#include "sha256_internal.h"

#include <stdlib.h>
#include <string.h>

enum { IO_BUFFER_BYTES = 4096 };
typedef enum { PARSE_NORMAL, PARSE_PAREN, PARSE_SEMICOLON } parse_mode;
typedef struct {
    parse_mode mode;
    size_t line_bytes;
    size_t pending_whitespace;
    size_t executable_lines;
    int line_started;
    int invalid;
} gcode_validator;

struct ed_verified_job {
    ed_storage storage;
    size_t size;
    unsigned char sha256[32];
    int used;
};

static int is_whitespace(unsigned char b) {
    return b == ' ' || b == '\t' || b == '\r';
}

static void finish_line(gcode_validator *v) {
    if (v->line_bytes > 0) v->executable_lines++;
    v->line_bytes = 0;
    v->pending_whitespace = 0;
    v->line_started = 0;
}

static void add_line_byte(gcode_validator *v, unsigned char b) {
    if (is_whitespace(b)) {
        if (v->line_started) v->pending_whitespace++;
        return;
    }
    if (v->line_bytes + v->pending_whitespace + 1 > 79) {
        v->invalid = 1;
        return;
    }
    v->line_bytes += v->pending_whitespace + 1;
    v->pending_whitespace = 0;
    v->line_started = 1;
}

static void validate_bytes(gcode_validator *v, const unsigned char *bytes,
                           size_t length) {
    size_t i;
    for (i = 0; i < length && !v->invalid; i++) {
        unsigned char b = bytes[i];
        if (b > 0x7e || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') || b == 0x7f) {
            v->invalid = 1;
            break;
        }
        if (v->mode == PARSE_SEMICOLON) {
            if (b == '\n') { v->mode = PARSE_NORMAL; finish_line(v); }
            continue;
        }
        if (v->mode == PARSE_PAREN) {
            if (b == '(') { v->invalid = 1; break; }
            if (b == ')') v->mode = PARSE_NORMAL;
            else if (b == '\n') finish_line(v);
            continue;
        }
        switch (b) {
        case '(':
            v->mode = PARSE_PAREN;
            break;
        case ')':
            v->invalid = 1;
            break;
        case ';':
            v->mode = PARSE_SEMICOLON;
            break;
        case '!': case '?': case '~': case 0x18:
            v->invalid = 1;
            break;
        case '\n':
            finish_line(v);
            break;
        default:
            add_line_byte(v, b);
            break;
        }
    }
}

static int digest_equal(const unsigned char a[32], const unsigned char b[32]) {
    unsigned char difference = 0;
    size_t i;
    for (i = 0; i < 32; i++) difference |= (unsigned char)(a[i] ^ b[i]);
    return difference == 0;
}

static ed_result discard_and_return(const ed_storage *storage, ed_result error) {
    (void)storage->reset(storage->context, 0);
    return error;
}

static ed_result discard_and_free(const ed_storage *storage, unsigned char *io,
                                  ed_result error) {
    free(io);
    return discard_and_return(storage, error);
}

ed_result ed_stage_verify(const ed_manifest *manifest, ed_range_fetch fetch,
                          void *fetch_context, const ed_storage *storage,
                          ed_verified_job **verified_out) {
    unsigned char *io, digest[32];
    size_t offset = 0, amount, received;
    ed_sha256_context hash;
    gcode_validator validator;
    ed_verified_job *job;
    if (verified_out == NULL) return ED_ERR_ARGUMENT;
    *verified_out = NULL;
    if (manifest == NULL || fetch == NULL || storage == NULL ||
        storage->reset == NULL || storage->write == NULL || storage->read == NULL ||
        storage->seal == NULL || storage->unseal == NULL)
        return ED_ERR_ARGUMENT;
    if (manifest->size > ED_MAX_PROGRAM_BYTES) {
        if (storage->reset(storage->context, 0) != 0) return ED_ERR_STORAGE;
        return ED_ERR_TOO_LARGE;
    }
    io = (unsigned char *)malloc(IO_BUFFER_BYTES);
    if (io == NULL) {
        return storage->reset(storage->context, 0) == 0 ? ED_ERR_MEMORY : ED_ERR_STORAGE;
    }
    if (storage->reset(storage->context, manifest->size) != 0) {
        free(io);
        return ED_ERR_STORAGE;
    }

    while (offset < manifest->size) {
        amount = manifest->size - offset;
        if (amount > ED_MAX_RANGE_BYTES) amount = ED_MAX_RANGE_BYTES;
        if (amount > IO_BUFFER_BYTES) amount = IO_BUFFER_BYTES;
        received = 0;
        if (fetch(fetch_context, offset, amount, io, IO_BUFFER_BYTES, &received) != 0)
            return discard_and_free(storage, io, ED_ERR_FETCH);
        if (received != amount) return discard_and_free(storage, io, ED_ERR_SIZE);
        if (storage->write(storage->context, offset, io, received) != 0)
            return discard_and_free(storage, io, ED_ERR_STORAGE);
        offset += received;
    }

    memset(&validator, 0, sizeof(validator));
    validator.mode = PARSE_NORMAL;
    ed_sha256_init_internal(&hash);
    offset = 0;
    while (offset < manifest->size) {
        amount = manifest->size - offset;
        if (amount > IO_BUFFER_BYTES) amount = IO_BUFFER_BYTES;
        if (storage->read(storage->context, offset, io, amount) != 0)
            return discard_and_free(storage, io, ED_ERR_STORAGE);
        ed_sha256_update_internal(&hash, io, amount);
        validate_bytes(&validator, io, amount);
        if (validator.invalid) return discard_and_free(storage, io, ED_ERR_GCODE);
        offset += amount;
    }
    ed_sha256_finish_internal(&hash, digest);
    if (!digest_equal(digest, manifest->sha256)) return discard_and_free(storage, io, ED_ERR_DIGEST);
    if (validator.mode == PARSE_PAREN) return discard_and_free(storage, io, ED_ERR_GCODE);
    finish_line(&validator);
    if (validator.executable_lines == 0) return discard_and_free(storage, io, ED_ERR_GCODE);

    job = (ed_verified_job *)malloc(sizeof(*job));
    if (job == NULL) return discard_and_free(storage, io, ED_ERR_MEMORY);
    if (storage->seal(storage->context, manifest->size) != 0) {
        free(job);
        storage->unseal(storage->context);
        return discard_and_free(storage, io, ED_ERR_STORAGE);
    }
    free(io);
    job->storage = *storage;
    job->size = manifest->size;
    memcpy(job->sha256, digest, sizeof(job->sha256));
    job->used = 0;
    *verified_out = job;
    return ED_OK;
}

ed_result ed_verified_send_uart(ed_verified_job *verified,
                                ed_uart_sink sink, void *sink_context) {
    unsigned char *io;
    size_t offset = 0, amount;
    unsigned char digest[32];
    ed_sha256_context hash;
    if (verified == NULL || sink == NULL || verified->used) return ED_ERR_ARGUMENT;
    verified->used = 1;
    io = (unsigned char *)malloc(IO_BUFFER_BYTES);
    if (io == NULL) return ED_ERR_MEMORY;
    /* Storage is an injected backend and may be mutable. Verify the complete
     * staged image before the first UART callback to preserve the gate. */
    ed_sha256_init_internal(&hash);
    while (offset < verified->size) {
        amount = verified->size - offset;
        if (amount > IO_BUFFER_BYTES) amount = IO_BUFFER_BYTES;
        if (verified->storage.read(verified->storage.context, offset, io, amount) != 0)
            { free(io); return ED_ERR_STORAGE; }
        ed_sha256_update_internal(&hash, io, amount);
        offset += amount;
    }
    ed_sha256_finish_internal(&hash, digest);
    if (!digest_equal(digest, verified->sha256)) { free(io); return ED_ERR_DIGEST; }
    offset = 0;
    while (offset < verified->size) {
        amount = verified->size - offset;
        if (amount > IO_BUFFER_BYTES) amount = IO_BUFFER_BYTES;
        if (verified->storage.read(verified->storage.context, offset, io, amount) != 0)
            { free(io); return ED_ERR_STORAGE; }
        if (sink(sink_context, io, amount) != 0) { free(io); return ED_ERR_UART; }
        offset += amount;
    }
    free(io);
    return ED_OK;
}

void ed_verified_release(ed_verified_job *verified) {
    if (verified != NULL) {
        verified->storage.unseal(verified->storage.context);
        free(verified);
    }
}

#include "job_core.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define STORE_CAPACITY (128u * 1024u)
typedef struct {
    unsigned char source[STORE_CAPACITY];
    unsigned char staged[STORE_CAPACITY];
    size_t source_size;
    size_t staged_size;
    size_t next_fetch;
    size_t fetch_calls;
    size_t max_requested;
    size_t forced_short_at;
    size_t fail_fetch_at;
    int bad_order;
    int sealed;
    int attempt_mutation_on_emit;
    int mutation_rejected;
    size_t reads;
    size_t emit_read_index;
} fixture;

typedef struct {
    unsigned char bytes[STORE_CAPACITY];
    size_t size;
} sink;

static int fetch_range(void *context, size_t offset, size_t max_length,
                       unsigned char *destination, size_t capacity,
                       size_t *received) {
    fixture *f = (fixture *)context;
    size_t amount;
    if (offset != f->next_fetch || offset > f->source_size || max_length > capacity) {
        f->bad_order = 1;
        return -1;
    }
    if (offset == f->fail_fetch_at) return -1;
    if (max_length > f->max_requested) f->max_requested = max_length;
    f->fetch_calls++;
    amount = f->source_size - offset;
    if (amount > max_length) amount = max_length;
    if (f->forced_short_at == offset && amount != 0) amount--;
    memcpy(destination, f->source + offset, amount);
    *received = amount;
    f->next_fetch += amount;
    return 0;
}

static int storage_reset(void *context, size_t expected_size) {
    fixture *f = (fixture *)context;
    (void)expected_size;
    f->staged_size = 0;
    return 0;
}

static int storage_write(void *context, size_t offset,
                         const unsigned char *bytes, size_t length) {
    fixture *f = (fixture *)context;
    if (f->sealed) return -1;
    if (offset + length > STORE_CAPACITY) return -1;
    memcpy(f->staged + offset, bytes, length);
    if (f->staged_size < offset + length) f->staged_size = offset + length;
    return 0;
}

static int storage_read(void *context, size_t offset, unsigned char *bytes,
                        size_t length) {
    fixture *f = (fixture *)context;
    if (f->sealed && f->attempt_mutation_on_emit && f->reads == f->emit_read_index) {
        unsigned char changed = (unsigned char)(f->staged[0] ^ 0xffu);
        f->mutation_rejected = storage_write(f, 0, &changed, 1) != 0;
        f->attempt_mutation_on_emit = 0;
    }
    if (offset + length > f->staged_size) return -1;
    memcpy(bytes, f->staged + offset, length);
    f->reads++;
    return 0;
}

static int storage_seal(void *context, size_t expected_size) {
    fixture *f = (fixture *)context;
    if (f->staged_size != expected_size) return -1;
    f->sealed = 1;
    return 0;
}

static void storage_unseal(void *context) {
    fixture *f = (fixture *)context;
    f->sealed = 0;
}

static int uart_write(void *context, const unsigned char *bytes, size_t length) {
    sink *s = (sink *)context;
    if (s->size + length > sizeof(s->bytes)) return -1;
    memcpy(s->bytes + s->size, bytes, length);
    s->size += length;
    return 0;
}

static int check(int condition, const char *message) {
    if (!condition) {
        fprintf(stderr, "FAIL: %s\n", message);
        return 0;
    }
    return 1;
}

static int prepare(fixture *f, const unsigned char *program, size_t length,
                   ed_manifest *manifest, ed_storage *storage) {
    memset(f, 0, sizeof(*f));
    f->forced_short_at = SIZE_MAX;
    f->fail_fetch_at = SIZE_MAX;
    memcpy(f->source, program, length);
    f->source_size = length;
    manifest->size = length;
    ed_sha256(program, length, manifest->sha256);
    storage->context = f;
    storage->reset = storage_reset;
    storage->write = storage_write;
    storage->read = storage_read;
    storage->seal = storage_seal;
    storage->unseal = storage_unseal;
    return 1;
}

static int test_valid_and_chunk_contract(void) {
    unsigned char program[70000];
    static const unsigned char line[] = "G1 X1\n";
    size_t length = 0;
    fixture f;
    sink output = {{0}, 0};
    ed_manifest manifest;
    ed_storage storage;
    ed_verified_job *verified = NULL;
    while (length + sizeof(line) - 1 <= sizeof(program)) {
        memcpy(program + length, line, sizeof(line) - 1);
        length += sizeof(line) - 1;
    }
    if (!prepare(&f, program, length, &manifest, &storage)) return 0;
    ed_result result = ed_stage_verify(&manifest, fetch_range, &f, &storage, &verified);
    if (result != ED_OK) fprintf(stderr, "valid verify result=%d fetch_calls=%zu next=%zu size=%zu\n", (int)result, f.fetch_calls, f.next_fetch, manifest.size);
    if (!check(result == ED_OK, "valid input verifies")) return 0;
    if (!check(verified != NULL, "verified handle returned")) return 0;
    if (!check(f.max_requested <= ED_MAX_RANGE_BYTES, "range requests are bounded")) return 0;
    if (!check(f.next_fetch == length && f.fetch_calls > 1 && !f.bad_order,
               "ranges arrive in exact offset order")) return 0;
    {
        unsigned char changed = (unsigned char)(f.staged[0] ^ 0xffu);
        if (!check(storage_write(&f, 0, &changed, 1) != 0,
                   "storage is immutable while the verified handle is alive")) return 0;
    }
    f.attempt_mutation_on_emit = 1;
    f.emit_read_index = 2u * ((length + 4095u) / 4096u);
    if (!check(ed_verified_send_uart(verified, uart_write, &output) == ED_OK,
               "verified bytes reach UART sink")) return 0;
    if (!check(output.size == length &&
               memcmp(output.bytes, program, output.size) == 0,
               "UART receives exact original bytes")) return 0;
    if (!check(f.mutation_rejected, "storage rejects mutation at emission read")) return 0;
    output.size = 0;
    if (!check(ed_verified_send_uart(verified, uart_write, &output) == ED_ERR_ARGUMENT,
               "verified handle cannot replay UART output")) return 0;
    if (!check(output.size == 0, "single-use handle emits no second copy")) return 0;
    ed_verified_release(verified);
    return 1;
}

static int expect_preverification_failure(const char *name, fixture *f,
        ed_manifest *manifest, ed_storage *storage) {
    sink output = {{0}, 0};
    ed_verified_job *verified = NULL;
    ed_result result = ed_stage_verify(manifest, fetch_range, f, storage, &verified);
    if (!check(result != ED_OK, name)) return 0;
    if (!check(verified == NULL, "failure returns no verified handle")) return 0;
    if (!check(ed_verified_send_uart(verified, uart_write, &output) == ED_ERR_ARGUMENT,
               "UART API rejects absent verified handle")) return 0;
    if (!check(output.size == 0, "UART sink remains empty before verification")) return 0;
    if (!check(f->staged_size == 0, "failed staging is discarded")) return 0;
    return 1;
}

static int test_short_transfer(void) {
    static const unsigned char program[] = "G1 X1\n";
    fixture f; ed_manifest manifest; ed_storage storage;
    prepare(&f, program, sizeof(program)-1, &manifest, &storage);
    f.forced_short_at = 0;
    return expect_preverification_failure("truncated transfer rejected", &f, &manifest, &storage);
}

static int test_digest_mismatch(void) {
    static const unsigned char program[] = "G1 X1\n";
    fixture f; ed_manifest manifest; ed_storage storage;
    prepare(&f, program, sizeof(program)-1, &manifest, &storage);
    manifest.sha256[0] ^= 0xff;
    return expect_preverification_failure("altered digest rejected", &f, &manifest, &storage);
}

static int test_malformed_gcode(void) {
    static const unsigned char program[] = "G1 X1\n(unterminated\n";
    fixture f; ed_manifest manifest; ed_storage storage;
    prepare(&f, program, sizeof(program)-1, &manifest, &storage);
    return expect_preverification_failure("malformed G-code rejected", &f, &manifest, &storage);
}

static int test_oversized_manifest(void) {
    static const unsigned char program[] = "G1 X1\n";
    static const unsigned char stale[] = "G1 X9\n";
    fixture f; ed_manifest manifest; ed_storage storage;
    prepare(&f, program, sizeof(program)-1, &manifest, &storage);
    if (storage_write(&f, 0, stale, sizeof(stale)-1) != 0) return 0;
    manifest.size = ED_MAX_PROGRAM_BYTES + 1u;
    ed_verified_job *verified = NULL;
    ed_result result = ed_stage_verify(&manifest, fetch_range, &f, &storage, &verified);
    if (!check(result == ED_ERR_TOO_LARGE, "oversized manifest rejected")) return 0;
    if (!check(verified == NULL, "oversized manifest yields no verified handle")) return 0;
    if (!check(f.fetch_calls == 0, "oversized manifest is rejected before fetching")) return 0;
    return check(f.staged_size == 0, "oversized manifest discards previous staged bytes");
}

static int test_out_of_order_fetch(void) {
    static const unsigned char program[] = "G1 X1\n";
    fixture f; ed_manifest manifest; ed_storage storage;
    prepare(&f, program, sizeof(program)-1, &manifest, &storage);
    f.next_fetch = 1;
    return expect_preverification_failure("out-of-order range rejected", &f, &manifest, &storage);
}

static int test_partial_stage_cleanup(void) {
    unsigned char program[10000];
    static const unsigned char line[] = "G1 X1\n";
    size_t length = 0;
    fixture f; ed_manifest manifest; ed_storage storage;
    while (length + sizeof(line) - 1 <= sizeof(program)) {
        memcpy(program + length, line, sizeof(line) - 1);
        length += sizeof(line) - 1;
    }
    prepare(&f, program, length, &manifest, &storage);
    f.fail_fetch_at = 4096;
    return expect_preverification_failure("failure after first range is rejected",
                                          &f, &manifest, &storage);
}

static int test_sha256_known_vector(void) {
    static const unsigned char expected[32] = {
        0xbau,0x78u,0x16u,0xbfu,0x8fu,0x01u,0xcfu,0xeau,
        0x41u,0x41u,0x40u,0xdeu,0x5du,0xaeu,0x22u,0x23u,
        0xb0u,0x03u,0x61u,0xa3u,0x96u,0x17u,0x7au,0x9cu,
        0xb4u,0x10u,0xffu,0x61u,0xf2u,0x00u,0x15u,0xadu
    };
    unsigned char actual[32];
    ed_sha256((const unsigned char *)"abc", 3, actual);
    return check(memcmp(actual, expected, sizeof(expected)) == 0,
                 "SHA-256 matches independent known vector");
}

int main(void) {
    int ok = 1;
    ok &= test_valid_and_chunk_contract();
    ok &= test_short_transfer();
    ok &= test_digest_mismatch();
    ok &= test_malformed_gcode();
    ok &= test_oversized_manifest();
    ok &= test_out_of_order_fetch();
    ok &= test_partial_stage_cleanup();
    ok &= test_sha256_known_vector();
    if (!ok) return EXIT_FAILURE;
    puts("all job core contract tests passed");
    return EXIT_SUCCESS;
}

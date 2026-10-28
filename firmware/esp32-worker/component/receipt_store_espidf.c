#include "receipt_store_espidf.h"

#include <string.h>

#include "esp_err.h"

#define RECEIPT_KEY "term_receipt"
#define RECEIPT_MAGIC 0x45445231u /* EDR1 */
#define RECEIPT_VERSION 1u

_Static_assert(sizeof(RECEIPT_KEY) <= 16u,
               "ESP-IDF NVS keys allow at most 15 characters");

/* The blob has an explicit byte layout, so compiler padding and size_t width
 * are not part of the persisted format. Integer fields are little endian. */
#define WIRE_HEADER_BYTES 12u
#define WIRE_SESSION_BYTES ED_WORKER_SESSION_BYTES
#define WIRE_JOB_BYTES ED_WORKER_JOB_ID_BYTES
#define WIRE_LEASE_BYTES ED_WORKER_LEASE_BYTES
#define WIRE_RECEIPT_STATE_BYTES 4u
#define WIRE_ACKED_BYTES 8u
#define WIRE_TOTAL_BYTES 8u
#define WIRE_IDLE_BYTES 1u
#define WIRE_CONTROLLER_BYTES ED_WORKER_CONTROLLER_STATE_BYTES
#define WIRE_ERROR_BYTES ED_WORKER_ERROR_BYTES
#define WIRE_CHECKSUM_BYTES 4u
#define WIRE_BYTES (WIRE_HEADER_BYTES + WIRE_SESSION_BYTES + WIRE_JOB_BYTES + \
                    WIRE_LEASE_BYTES + WIRE_RECEIPT_STATE_BYTES + \
                    WIRE_ACKED_BYTES + WIRE_TOTAL_BYTES + WIRE_IDLE_BYTES + \
                    WIRE_CONTROLLER_BYTES + WIRE_ERROR_BYTES + \
                    WIRE_CHECKSUM_BYTES)

static int has_nul(const char *text, size_t size) {
    return text != NULL && memchr(text, '\0', size) != NULL;
}

static void put_u32(unsigned char *out, uint32_t value) {
    out[0] = (unsigned char)value;
    out[1] = (unsigned char)(value >> 8);
    out[2] = (unsigned char)(value >> 16);
    out[3] = (unsigned char)(value >> 24);
}

static uint32_t get_u32(const unsigned char *in) {
    return (uint32_t)in[0] | ((uint32_t)in[1] << 8) |
           ((uint32_t)in[2] << 16) | ((uint32_t)in[3] << 24);
}

static void put_u64(unsigned char *out, uint64_t value) {
    for (size_t i = 0; i < 8; ++i) out[i] = (unsigned char)(value >> (i * 8));
}

static uint64_t get_u64(const unsigned char *in) {
    uint64_t value = 0;
    for (size_t i = 0; i < 8; ++i) value |= (uint64_t)in[i] << (i * 8);
    return value;
}

static uint32_t checksum(const unsigned char *bytes, size_t length) {
    uint32_t crc = 0xffffffffu;
    for (size_t i = 0; i < length; ++i) {
        crc ^= bytes[i];
        for (unsigned bit = 0; bit < 8; ++bit)
            crc = (crc >> 1) ^ (0xedb88320u & (uint32_t)-(int32_t)(crc & 1u));
    }
    return ~crc;
}

static int receipt_valid(const ed_worker_saved_receipt *receipt) {
    const ed_terminal_receipt *terminal;
    if (receipt == NULL || !has_nul(receipt->session, sizeof(receipt->session)) ||
        !has_nul(receipt->job_id, sizeof(receipt->job_id)) ||
        !has_nul(receipt->lease, sizeof(receipt->lease)) ||
        receipt->session[0] == '\0' || receipt->job_id[0] == '\0' ||
        receipt->lease[0] == '\0') return 0;
    terminal = &receipt->receipt;
    return (terminal->state == ED_TERMINAL_COMPLETED ||
            terminal->state == ED_TERMINAL_FAILED ||
            terminal->state == ED_TERMINAL_CANCELED) &&
           terminal->acked <= terminal->total &&
           (terminal->controller_idle == 0 || terminal->controller_idle == 1) &&
           has_nul(terminal->controller_state,
                   sizeof(terminal->controller_state)) &&
           has_nul(terminal->error, sizeof(terminal->error));
}

static void encode(const ed_worker_saved_receipt *receipt,
                   unsigned char out[WIRE_BYTES]) {
    size_t at = 0;
    memset(out, 0, WIRE_BYTES);
    put_u32(out + at, RECEIPT_MAGIC); at += 4;
    put_u32(out + at, RECEIPT_VERSION); at += 4;
    put_u32(out + at, WIRE_BYTES); at += 4;
    memcpy(out + at, receipt->session, WIRE_SESSION_BYTES); at += WIRE_SESSION_BYTES;
    memcpy(out + at, receipt->job_id, WIRE_JOB_BYTES); at += WIRE_JOB_BYTES;
    memcpy(out + at, receipt->lease, WIRE_LEASE_BYTES); at += WIRE_LEASE_BYTES;
    put_u32(out + at, (uint32_t)receipt->receipt.state); at += WIRE_RECEIPT_STATE_BYTES;
    put_u64(out + at, (uint64_t)receipt->receipt.acked); at += WIRE_ACKED_BYTES;
    put_u64(out + at, (uint64_t)receipt->receipt.total); at += WIRE_TOTAL_BYTES;
    out[at++] = (unsigned char)receipt->receipt.controller_idle;
    memcpy(out + at, receipt->receipt.controller_state, WIRE_CONTROLLER_BYTES);
    at += WIRE_CONTROLLER_BYTES;
    memcpy(out + at, receipt->receipt.error, WIRE_ERROR_BYTES); at += WIRE_ERROR_BYTES;
    put_u32(out + at, checksum(out, at));
}

static int decode(const unsigned char in[WIRE_BYTES],
                  ed_worker_saved_receipt *receipt) {
    size_t at = 0;
    uint64_t acked, total;
    if (get_u32(in) != RECEIPT_MAGIC || get_u32(in + 4) != RECEIPT_VERSION ||
        get_u32(in + 8) != WIRE_BYTES ||
        get_u32(in + WIRE_BYTES - WIRE_CHECKSUM_BYTES) !=
            checksum(in, WIRE_BYTES - WIRE_CHECKSUM_BYTES)) return -1;
    memset(receipt, 0, sizeof(*receipt));
    at = WIRE_HEADER_BYTES;
    memcpy(receipt->session, in + at, WIRE_SESSION_BYTES); at += WIRE_SESSION_BYTES;
    memcpy(receipt->job_id, in + at, WIRE_JOB_BYTES); at += WIRE_JOB_BYTES;
    memcpy(receipt->lease, in + at, WIRE_LEASE_BYTES); at += WIRE_LEASE_BYTES;
    receipt->receipt.state = (ed_terminal_state)get_u32(in + at); at += WIRE_RECEIPT_STATE_BYTES;
    acked = get_u64(in + at); at += WIRE_ACKED_BYTES;
    total = get_u64(in + at); at += WIRE_TOTAL_BYTES;
    if (acked > SIZE_MAX || total > SIZE_MAX) return -1;
    receipt->receipt.acked = (size_t)acked;
    receipt->receipt.total = (size_t)total;
    receipt->receipt.controller_idle = in[at++];
    memcpy(receipt->receipt.controller_state, in + at, WIRE_CONTROLLER_BYTES);
    at += WIRE_CONTROLLER_BYTES;
    memcpy(receipt->receipt.error, in + at, WIRE_ERROR_BYTES);
    return receipt_valid(receipt) ? 0 : -1;
}

static int store_load(void *context, ed_worker_saved_receipt *receipt,
                      int *present) {
    ed_espidf_receipt_store *store = (ed_espidf_receipt_store *)context;
    unsigned char wire[WIRE_BYTES];
    size_t length = sizeof(wire);
    esp_err_t error;
    if (store == NULL || !store->is_open || receipt == NULL || present == NULL)
        return -1;
    *present = 0;
    error = nvs_get_blob(store->handle, RECEIPT_KEY, NULL, &length);
    if (error == ESP_ERR_NVS_NOT_FOUND) return 0;
    if (error != ESP_OK || length != sizeof(wire)) return -1;
    if (nvs_get_blob(store->handle, RECEIPT_KEY, wire, &length) != ESP_OK ||
        decode(wire, receipt) != 0) return -1;
    *present = 1;
    return 0;
}

static int store_save(void *context, const ed_worker_saved_receipt *receipt) {
    ed_espidf_receipt_store *store = (ed_espidf_receipt_store *)context;
    unsigned char wire[WIRE_BYTES];
    if (store == NULL || !store->is_open || !receipt_valid(receipt)) return -1;
    encode(receipt, wire);
    return nvs_set_blob(store->handle, RECEIPT_KEY, wire, sizeof(wire)) == ESP_OK &&
                   nvs_commit(store->handle) == ESP_OK ? 0 : -1;
}

static int store_clear(void *context) {
    ed_espidf_receipt_store *store = (ed_espidf_receipt_store *)context;
    esp_err_t error;
    if (store == NULL || !store->is_open) return -1;
    error = nvs_erase_key(store->handle, RECEIPT_KEY);
    if (error == ESP_ERR_NVS_NOT_FOUND) return 0;
    return error == ESP_OK && nvs_commit(store->handle) == ESP_OK ? 0 : -1;
}

ed_result ed_espidf_receipt_store_init(ed_espidf_receipt_store *store,
                                       const char *name_space) {
    if (store == NULL || name_space == NULL || name_space[0] == '\0' ||
        strlen(name_space) > 15u) return ED_ERR_ARGUMENT;
    memset(store, 0, sizeof(*store));
    if (nvs_open(name_space, NVS_READWRITE, &store->handle) != ESP_OK)
        return ED_ERR_STORAGE;
    store->is_open = 1;
    return ED_OK;
}

void ed_espidf_receipt_store_deinit(ed_espidf_receipt_store *store) {
    if (store != NULL && store->is_open) {
        nvs_close(store->handle);
        memset(store, 0, sizeof(*store));
    }
}

ed_worker_receipt_store ed_espidf_receipt_store_adapter(
    ed_espidf_receipt_store *store) {
    ed_worker_receipt_store adapter = {0};
    adapter.context = store;
    adapter.load = store_load;
    adapter.save = store_save;
    adapter.clear = store_clear;
    return adapter;
}

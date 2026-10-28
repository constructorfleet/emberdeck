#ifndef EMBERDECK_RECEIPT_STORE_ESP_IDF_H
#define EMBERDECK_RECEIPT_STORE_ESP_IDF_H

#include "worker_cycle.h"

#include "nvs.h"

/* Durable terminal-receipt outbox backed by one NVS blob. The NVS partition
 * must already be initialized by the application. Keep one instance for the
 * lifetime of the worker task. */
typedef struct {
    nvs_handle_t handle;
    int is_open;
} ed_espidf_receipt_store;

ed_result ed_espidf_receipt_store_init(ed_espidf_receipt_store *store,
                                       const char *name_space);
void ed_espidf_receipt_store_deinit(ed_espidf_receipt_store *store);
ed_worker_receipt_store ed_espidf_receipt_store_adapter(
    ed_espidf_receipt_store *store);

#endif

#include "http_client_espidf.h"
#include "receipt_store_espidf.h"

/* Compile/link smoke only. This entry point does not start Wi-Fi, open a
 * session, contact a server, or write to a controller. */
void app_main(void) {
    ed_http_worker_client client;
    ed_espidf_receipt_store store;
    (void)ed_http_worker_client_init_espidf(&client,
        "https://worker.invalid", "compile-only-key", "compile-only-driver");
    (void)ed_espidf_receipt_store_init(&store, "emberdeck");
    ed_espidf_receipt_store_deinit(&store);
}

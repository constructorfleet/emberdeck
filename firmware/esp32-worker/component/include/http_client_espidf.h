#ifndef EMBERDECK_HTTP_CLIENT_ESP_IDF_H
#define EMBERDECK_HTTP_CLIENT_ESP_IDF_H

#include "http_client.h"

/* Initializes the common worker client with ESP-IDF's HTTPS transport, the
 * built-in certificate bundle, and the hardware random number generator.
 * Credentials remain caller-provisioned; Wi-Fi and controller setup are out
 * of scope for this transport adapter. */
ed_result ed_http_worker_client_init_espidf(ed_http_worker_client *client,
                                            const char *server_url,
                                            const char *worker_key,
                                            const char *driver_id);

#endif

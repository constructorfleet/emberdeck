idf_component_register(
    SRCS "job_core.c" "worker_cycle.c" "http_client.c" "http_client_espidf.c"
         "receipt_store_espidf.c" "sha256.c"
    INCLUDE_DIRS "include"
    REQUIRES esp_http_client mbedtls nvs_flash)

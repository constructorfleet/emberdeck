#include "http_client_espidf.h"

#include <limits.h>
#include <string.h>
#include <strings.h>

#include "esp_crt_bundle.h"
#include "esp_http_client.h"
#include "esp_random.h"

#define ED_IDF_HTTP_TIMEOUT_MS 15000
#define ED_IDF_HTTP_RX_BUFFER_BYTES 1024
#define ED_IDF_HTTP_TX_BUFFER_BYTES 1024

typedef struct {
    ed_http_response *response;
    int failed;
} ed_idf_http_event_context;

static int is_https_url(const char *url) {
    return url != NULL && strncmp(url, "https://", 8) == 0 && url[8] != '\0';
}

static esp_err_t on_http_event(esp_http_client_event_t *event) {
    ed_idf_http_event_context *context = event != NULL ? event->user_data : NULL;
    ed_http_response *response;
    if (context == NULL || context->response == NULL || event == NULL) return ESP_FAIL;
    response = context->response;
    switch (event->event_id) {
    case HTTP_EVENT_ON_HEADER: {
        ed_http_response_header *header;
        size_t name_length, value_length, i;
        int relevant;
        if (event->header_key == NULL || event->header_value == NULL) {
            context->failed = 1;
            return ESP_FAIL;
        }
        relevant = strcasecmp(event->header_key, "Content-Type") == 0 ||
                   strcasecmp(event->header_key, "Content-Range") == 0 ||
                   strcasecmp(event->header_key, "Content-Length") == 0 ||
                   strcasecmp(event->header_key, "X-EmberDeck-Content-SHA256") == 0;
        if (!relevant) break;
        name_length = strlen(event->header_key);
        value_length = strlen(event->header_value);
        if (name_length == 0 || name_length >= ED_HTTP_HEADER_NAME_BYTES ||
            value_length >= ED_HTTP_HEADER_VALUE_BYTES ||
            response->header_count >= ED_HTTP_RESPONSE_HEADERS) {
            context->failed = 1;
            return ESP_FAIL;
        }
        for (i = 0; i < response->header_count; i++) {
            if (strcasecmp(response->headers[i].name, event->header_key) == 0) {
                /* Ambiguous security-relevant response metadata is rejected. */
                context->failed = 1;
                return ESP_FAIL;
            }
        }
        header = &response->headers[response->header_count++];
        memcpy(header->name, event->header_key, name_length + 1);
        memcpy(header->value, event->header_value, value_length + 1);
        break;
    }
    case HTTP_EVENT_ON_DATA:
        if (event->data_len < 0 ||
            (size_t)event->data_len > response->body_capacity - response->body_length) {
            context->failed = 1;
            return ESP_FAIL;
        }
        if (event->data_len > 0) {
            if (event->data == NULL) {
                context->failed = 1;
                return ESP_FAIL;
            }
            memcpy(response->body + response->body_length, event->data,
                   (size_t)event->data_len);
        }
        response->body_length += (size_t)event->data_len;
        break;
    case HTTP_EVENT_ERROR:
        context->failed = 1;
        break;
    default:
        break;
    }
    return ESP_OK;
}

static esp_http_client_method_t method_for(const char *method) {
    if (strcmp(method, "GET") == 0) return HTTP_METHOD_GET;
    if (strcmp(method, "POST") == 0) return HTTP_METHOD_POST;
    return HTTP_METHOD_MAX;
}

static int idf_transport(void *unused, const ed_http_request *request,
                         ed_http_response *response) {
    esp_http_client_config_t config = {0};
    ed_idf_http_event_context event_context;
    esp_http_client_handle_t client;
    esp_err_t result;
    size_t i;
    (void)unused;
    if (request == NULL || response == NULL || request->method == NULL ||
        request->url == NULL || request->follow_redirects != 0 ||
        !is_https_url(request->url) || response->body == NULL ||
        response->body_capacity == 0 ||
        (request->header_count > 0 && request->headers == NULL)) return -1;

    response->status = 0;
    response->body_length = 0;
    response->header_count = 0;
    event_context.response = response;
    event_context.failed = 0;
    config.url = request->url;
    config.event_handler = on_http_event;
    config.user_data = &event_context;
    config.crt_bundle_attach = esp_crt_bundle_attach;
    config.disable_auto_redirect = true;
    config.timeout_ms = ED_IDF_HTTP_TIMEOUT_MS;
    config.buffer_size = ED_IDF_HTTP_RX_BUFFER_BYTES;
    config.buffer_size_tx = ED_IDF_HTTP_TX_BUFFER_BYTES;
    client = esp_http_client_init(&config);
    if (client == NULL) return -1;
    if (method_for(request->method) == HTTP_METHOD_MAX ||
        esp_http_client_set_method(client, method_for(request->method)) != ESP_OK) {
        esp_http_client_cleanup(client);
        return -1;
    }
    for (i = 0; i < request->header_count; i++) {
        if (request->headers[i].name == NULL || request->headers[i].value == NULL ||
            esp_http_client_set_header(client, request->headers[i].name,
                                       request->headers[i].value) != ESP_OK) {
            esp_http_client_cleanup(client);
            return -1;
        }
    }
    if (strcmp(request->method, "POST") == 0) {
        if (request->body == NULL || request->body_length > INT_MAX ||
            esp_http_client_set_post_field(client,
                (const char *)request->body, (int)request->body_length) != ESP_OK) {
            esp_http_client_cleanup(client);
            return -1;
        }
    } else if (request->body_length != 0) {
        esp_http_client_cleanup(client);
        return -1;
    }
    result = esp_http_client_perform(client);
    if (result == ESP_OK && !event_context.failed) {
        response->status = esp_http_client_get_status_code(client);
    } else {
        response->body_length = 0;
    }
    esp_http_client_cleanup(client);
    return result == ESP_OK && !event_context.failed && response->status > 0 ? 0 : -1;
}

static int idf_random_bytes(void *unused, unsigned char *bytes, size_t length) {
    (void)unused;
    if (bytes == NULL && length != 0) return -1;
    esp_fill_random(bytes, length);
    return 0;
}

ed_result ed_http_worker_client_init_espidf(ed_http_worker_client *client,
                                            const char *server_url,
                                            const char *worker_key,
                                            const char *driver_id) {
    return ed_http_worker_client_init(client, server_url, worker_key, driver_id,
                                      idf_transport, idf_random_bytes, NULL);
}

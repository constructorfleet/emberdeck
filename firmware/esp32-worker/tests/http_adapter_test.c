#include "http_client.h"

#include <stdio.h>
#include <string.h>

#define TEST_JOB_ID "0123456789abcdef0123456789abcdef"
#define TEST_LEASE "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

typedef enum { RANGE_GOOD, RANGE_BAD_CONTENT_RANGE, RANGE_TRUNCATED, RANGE_BAD_DIGEST } range_mode;

typedef struct {
    ed_http_worker_client *client;
    const unsigned char *program;
    size_t program_size;
    char session[ED_WORKER_SESSION_BYTES];
    char digest[65];
    range_mode mode;
    int wrong_active_lease;
    int wrong_active_job;
    int bad_active_command;
    int lose_first_terminal_response;
    int terminal_was_lost;
    int request_count;
    size_t dispatched_total;
    int range_count;
    int active_count;
    int terminal_count;
    int close_count;
    int bad_request;
    char first_terminal_body[512];
    char last_terminal_body[512];
    int initial_poll_count;
    int terminal_applied;
    int terminal_application_count;
    int lose_terminal_acks;
    int lost_terminal_acks;
    int random_call_count;
    char first_session[ED_WORKER_SESSION_BYTES];
} fake_server;

typedef struct {
    int present;
    ed_worker_saved_receipt receipt;
    int save_count;
    int clear_count;
} fake_receipt_store;

typedef struct {
    unsigned char staged[128];
    size_t staged_size;
    int sealed;
} fake_storage;

typedef struct {
    fake_server *server;
    unsigned char uart[128];
    size_t uart_size;
    size_t total_lines;
    int stream_calls;
    int active_failed;
    size_t acked_lines;
    ed_active_poll active_poll;
    void *poll_context;
} fake_controller;

static int check(int condition, const char *message) {
    if (condition) return 1;
    fprintf(stderr, "FAIL: %s\n", message);
    return 0;
}

static const char *request_header(const ed_http_request *request, const char *name) {
    size_t i;
    for (i = 0; i < request->header_count; i++)
        if (strcmp(request->headers[i].name, name) == 0) return request->headers[i].value;
    return NULL;
}

static int set_response_header(ed_http_response *response, const char *name,
                               const char *value) {
    ed_http_response_header *header;
    if (response->header_count >= ED_HTTP_RESPONSE_HEADERS) return -1;
    header = &response->headers[response->header_count++];
    if (snprintf(header->name, sizeof(header->name), "%s", name) < 0 ||
        snprintf(header->value, sizeof(header->value), "%s", value) < 0) return -1;
    return 0;
}

static int response_json(ed_http_response *response, const char *body) {
    size_t length = strlen(body);
    if (length > response->body_capacity) return -1;
    memcpy(response->body, body, length);
    response->body_length = length;
    response->status = 200;
    response->header_count = 0;
    return 0;
}

static int random_bytes(void *context, unsigned char *bytes, size_t length) {
    fake_server *server = (fake_server *)context;
    size_t i;
    server->random_call_count++;
    for (i = 0; i < length; i++)
        bytes[i] = (unsigned char)(0x10 + server->random_call_count);
    return 0;
}

static int fake_http(void *context, const ed_http_request *request,
                     ed_http_response *response) {
    fake_server *server = (fake_server *)context;
    char body[ED_HTTP_RESPONSE_BYTES];
    const char *auth = request_header(request, "Authorization");
    const char *session = request_header(request, "X-EmberDeck-Session");
    const char *lease = request_header(request, "X-EmberDeck-Lease");
    server->request_count++;
    if (response != &server->client->http_response_workspace) {
        server->bad_request = 1;
        return -1;
    }
    response->header_count = 0;
    if (request->follow_redirects != 0 || auth == NULL || strcmp(auth, "Bearer device-key") != 0) {
        server->bad_request = 1;
        return -1;
    }
    if (strcmp(request->method, "GET") == 0) {
        const char *range = request_header(request, "Range");
        size_t start, end, requested;
        size_t body_length;
        char content_range[96], content_length[32];
        if (strstr(request->url, "/api/v1/drivers/laser-1/jobs/" TEST_JOB_ID "/content") == NULL ||
            session == NULL || strcmp(session, server->session) != 0 ||
            lease == NULL || strcmp(lease, TEST_LEASE) != 0 || range == NULL ||
            sscanf(range, "bytes=%zu-%zu", &start, &end) != 2 || start != 0 ||
            end + 1 != server->program_size) {
            server->bad_request = 1;
            return -1;
        }
        server->range_count++;
        requested = end - start + 1;
        body_length = requested;
        if (server->mode == RANGE_TRUNCATED && body_length > 0) body_length--;
        memcpy(response->body, server->program + start, body_length);
        response->body_length = body_length;
        response->status = 206;
        snprintf(content_range, sizeof(content_range), "bytes %zu-%zu/%zu", start, end,
                 server->program_size);
        if (server->mode == RANGE_BAD_CONTENT_RANGE) strcpy(content_range, "bytes 1-2/4");
        snprintf(content_length, sizeof(content_length), "%zu", requested);
        set_response_header(response, "Content-Type", "application/octet-stream");
        set_response_header(response, "Content-Range", content_range);
        set_response_header(response, "Content-Length", content_length);
        set_response_header(response, "X-EmberDeck-Content-SHA256", server->digest);
        return 0;
    }
    if (strcmp(request->method, "POST") != 0 || request->url == NULL ||
        strcmp(request->url, "https://cutter.test/api/v1/drivers/laser-1/poll") != 0 ||
        request->body_length >= sizeof(body)) {
        server->bad_request = 1;
        return -1;
    }
    memcpy(body, request->body, request->body_length);
    body[request->body_length] = '\0';
    if (strstr(body, "\"job_id\"") == NULL) {
        const char *session_field = strstr(body, "\"session\":\"");
        if (session_field == NULL || strstr(body, "\"chunked_content_v1\"") == NULL ||
            strstr(body, "\"capabilities\"") == NULL) {
            server->bad_request = 1;
            return -1;
        }
        session_field += strlen("\"session\":\"");
        if (sscanf(session_field, "%32[0-9a-f]", server->session) != 1) return -1;
        if (server->first_session[0] == '\0') strcpy(server->first_session, server->session);
        server->initial_poll_count++;
        if (server->terminal_applied) return response_json(response, "{\"command\":\"idle\"}");
        if (snprintf(body, sizeof(body),
                     "{\"command\":\"run\",\"protocol\":2,\"lease\":\"%s\","
                     "\"job\":{\"id\":\"%s\",\"total\":%zu,\"size\":%zu,\"content\":{\"size\":%zu,\"sha256\":\"%s\"}}}",
                     TEST_LEASE, TEST_JOB_ID, server->dispatched_total,
                     server->program_size, server->program_size,
                     server->digest) < 0) return -1;
        return response_json(response, body);
    }
    if (strstr(body, "\"state\":\"running\"") != NULL) {
        char active_lease[65];
        char active_session[ED_WORKER_SESSION_BYTES + 16];
        snprintf(active_session, sizeof(active_session), "\"session\":\"%s\"", server->session);
        server->active_count++;
        if (strstr(body, active_session) == NULL ||
            strstr(body, TEST_JOB_ID) == NULL || strstr(body, TEST_LEASE) == NULL ||
            strstr(body, "\"controller_state\"") == NULL) {
            server->bad_request = 1;
            return -1;
        }
        strcpy(active_lease, server->wrong_active_lease ?
               "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" : TEST_LEASE);
        snprintf(body, sizeof(body),
                 "{\"command\":\"%s\",\"lease\":\"%s\",\"job\":{\"id\":\"%s\"}}",
                 server->bad_active_command ? "pause" : "run", active_lease,
                 server->wrong_active_job ? "ffffffffffffffffffffffffffffffff" : TEST_JOB_ID);
        return response_json(response, body);
    }
    server->terminal_count++;
    {
        char terminal_session[ED_WORKER_SESSION_BYTES + 16];
        char expected_total[32];
        snprintf(terminal_session, sizeof(terminal_session), "\"session\":\"%s\"", server->session);
        snprintf(expected_total, sizeof(expected_total), "\"total\":%zu", server->dispatched_total);
        if (strstr(body, terminal_session) == NULL ||
        strstr(body, TEST_JOB_ID) == NULL || strstr(body, TEST_LEASE) == NULL ||
        strstr(body, expected_total) == NULL ||
        (strstr(body, "\"state\":\"completed\"") == NULL &&
         strstr(body, "\"state\":\"failed\"") == NULL)) {
            server->bad_request = 1;
            return -1;
        }
    }
    if (server->first_terminal_body[0] == '\0') strcpy(server->first_terminal_body, body);
    else if (strcmp(server->first_terminal_body, body) != 0) server->bad_request = 1;
    strcpy(server->last_terminal_body, body);
    if (!server->terminal_applied) {
        server->terminal_applied = 1;
        server->terminal_application_count++;
    }
    if (server->lose_terminal_acks > server->lost_terminal_acks) {
        server->lost_terminal_acks++;
        return -1;
    }
    if (server->lose_first_terminal_response && !server->terminal_was_lost) {
        server->terminal_was_lost = 1;
        return -1;
    }
    return response_json(response, "{\"command\":\"idle\"}");
}

static int receipt_store_load(void *context, ed_worker_saved_receipt *receipt,
                              int *present) {
    fake_receipt_store *store = (fake_receipt_store *)context;
    *present = store->present;
    if (store->present) *receipt = store->receipt;
    return 0;
}

static int receipt_store_save(void *context, const ed_worker_saved_receipt *receipt) {
    fake_receipt_store *store = (fake_receipt_store *)context;
    store->receipt = *receipt;
    store->present = 1;
    store->save_count++;
    return 0;
}

static int receipt_store_clear(void *context) {
    fake_receipt_store *store = (fake_receipt_store *)context;
    store->present = 0;
    store->clear_count++;
    return 0;
}

static int storage_reset(void *context, size_t expected_size) {
    fake_storage *storage = (fake_storage *)context;
    if (expected_size > sizeof(storage->staged)) return -1;
    storage->staged_size = 0;
    storage->sealed = 0;
    return 0;
}

static int storage_write(void *context, size_t offset, const unsigned char *bytes,
                         size_t length) {
    fake_storage *storage = (fake_storage *)context;
    if (storage->sealed || offset + length > sizeof(storage->staged)) return -1;
    memcpy(storage->staged + offset, bytes, length);
    if (storage->staged_size < offset + length) storage->staged_size = offset + length;
    return 0;
}

static int storage_read(void *context, size_t offset, unsigned char *bytes, size_t length) {
    fake_storage *storage = (fake_storage *)context;
    if (offset + length > storage->staged_size) return -1;
    memcpy(bytes, storage->staged + offset, length);
    return 0;
}

static int storage_seal(void *context, size_t expected_size) {
    fake_storage *storage = (fake_storage *)context;
    if (expected_size != storage->staged_size) return -1;
    storage->sealed = 1;
    return 0;
}

static void storage_unseal(void *context) {
    ((fake_storage *)context)->sealed = 0;
}

static int uart_sink(void *context, const unsigned char *bytes, size_t length) {
    fake_controller *controller = (fake_controller *)context;
    size_t i;
    if (controller->uart_size + length > sizeof(controller->uart)) return -1;
    for (i = 0; i < length; i++) {
        controller->uart[controller->uart_size++] = bytes[i];
        if (bytes[i] == '\n') {
            ed_active_command command;
            controller->acked_lines++;
            if (controller->active_poll(controller->poll_context, controller->acked_lines,
                                        controller->total_lines,
                                        controller->acked_lines == controller->total_lines ? "Idle" : "Run",
                                        &command) != 0 || command != ED_ACTIVE_CONTINUE) {
                controller->active_failed = 1;
                return -1;
            }
        }
    }
    return 0;
}

static int stream_verified(void *context, ed_verified_job *verified,
                           ed_active_poll active_poll, void *poll_context,
                           ed_terminal_receipt *receipt) {
    fake_controller *controller = (fake_controller *)context;
    ed_result sent;
    controller->stream_calls++;
    controller->active_poll = active_poll;
    controller->poll_context = poll_context;
    sent = ed_verified_send_uart(verified, uart_sink, controller);
    if (sent != ED_OK || controller->active_failed) return -1;
    receipt->state = ED_TERMINAL_COMPLETED;
    receipt->acked = controller->total_lines;
    receipt->total = controller->total_lines;
    receipt->controller_idle = 1;
    strcpy(receipt->controller_state, "Idle");
    return 0;
}

static size_t line_count(const unsigned char *program, size_t length) {
    size_t i, lines = 0;
    for (i = 0; i < length; i++) if (program[i] == '\n') lines++;
    return lines;
}

static int run_scenario(range_mode mode, int lose_receipt, int wrong_active,
                        int expect_success) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fake_server server = {0};
    fake_storage storage = {0};
    fake_controller controller = {0};
    fake_receipt_store saved = {0};
    ed_http_worker_client client;
    ed_worker_adapters adapters = {0};
    ed_worker_result result;
    unsigned char digest[32];
    char digest_hex[65];
    size_t i;
    ed_result status;
    int ok = 1;
    server.program = program;
    server.program_size = sizeof(program) - 1;
    server.dispatched_total = line_count(program, sizeof(program) - 1);
    server.mode = mode;
    server.lose_first_terminal_response = lose_receipt;
    server.wrong_active_lease = wrong_active == 1;
    server.wrong_active_job = wrong_active == 2;
    server.bad_active_command = wrong_active == 3;
    server.active_count = 0;
    controller.server = &server;
    controller.total_lines = line_count(program, sizeof(program) - 1);
    ed_sha256(program, sizeof(program) - 1, digest);
    for (i = 0; i < sizeof(digest); i++) {
        static const char hex[] = "0123456789abcdef";
        digest_hex[i * 2] = hex[digest[i] >> 4];
        digest_hex[i * 2 + 1] = hex[digest[i] & 15u];
    }
    digest_hex[64] = '\0';
    if (mode == RANGE_BAD_DIGEST) digest_hex[0] = digest_hex[0] == '0' ? '1' : '0';
    strcpy(server.digest, digest_hex);
    if (ed_http_worker_client_init(&client, "https://cutter.test", "device-key",
                                   "laser-1", fake_http, random_bytes, &server) != ED_OK ||
        ed_http_worker_adapter_init(&client, &adapters.http) != ED_OK) return 0;
    server.client = &client;
    adapters.controller.context = &controller;
    adapters.controller.stream_verified = stream_verified;
    adapters.storage.context = &storage;
    adapters.storage.reset = storage_reset;
    adapters.storage.write = storage_write;
    adapters.storage.read = storage_read;
    adapters.storage.seal = storage_seal;
    adapters.storage.unseal = storage_unseal;
    adapters.receipt_store.context = &saved;
    adapters.receipt_store.load = receipt_store_load;
    adapters.receipt_store.save = receipt_store_save;
    adapters.receipt_store.clear = receipt_store_clear;
    status = ed_worker_run_one(&adapters, &result);
    if (expect_success == 1) {
        ok &= check(status == ED_OK && result.outcome == ED_WORKER_COMPLETED,
                    "valid HTTP assignment completes through the worker coordinator");
        ok &= check(controller.uart_size == sizeof(program) - 1 &&
                    memcmp(controller.uart, program, sizeof(program) - 1) == 0,
                    "verified content reaches the fake UART exactly once");
        ok &= check(server.active_count == (int)controller.total_lines,
                    "each acknowledged line reports monotonic active progress");
        ok &= check(server.terminal_count == 2 && server.terminal_was_lost &&
                    strcmp(server.first_terminal_body, server.last_terminal_body) == 0,
                    "lost terminal response retries the identical receipt");
    } else if (expect_success == 2) {
        ok &= check(status != ED_OK && controller.stream_calls == 1 &&
                    server.terminal_count == 1 &&
                    strstr(server.last_terminal_body, "\"state\":\"failed\"") != NULL,
                    "invalid active response stops the stream and reports failure");
    } else {
        ok &= check(status != ED_OK && controller.uart_size == 0 &&
                    controller.stream_calls == 0,
                    "bad transfer fails before the controller or UART is opened");
        ok &= check(server.terminal_count == 1 &&
                    strstr(server.last_terminal_body, "\"state\":\"failed\"") != NULL,
                    "bad transfer reports a failed terminal receipt under its lease");
    }
    ok &= check(!server.bad_request && server.range_count > 0,
                "HTTP requests preserve session, job, lease, auth, and bounded range");
    {
        int requests_before_close = server.request_count;
        adapters.http.close_session(adapters.http.context, server.session);
        ok &= check(server.request_count == requests_before_close &&
                    client.has_active_assignment == 0,
                    "close_session clears local state without making an HTTP request");
    }
    return ok;
}

static int test_durable_terminal_receipt_retry(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fake_server server = {0};
    fake_storage storage = {0};
    fake_controller controller = {0};
    fake_receipt_store saved = {0};
    ed_http_worker_client client;
    ed_worker_adapters adapters = {0};
    ed_worker_result result;
    unsigned char digest[32];
    char digest_hex[65];
    size_t i;
    ed_result first_status, retry_status;
    server.program = program;
    server.program_size = sizeof(program) - 1;
    server.dispatched_total = line_count(program, sizeof(program) - 1);
    server.lose_terminal_acks = 3;
    controller.server = &server;
    controller.total_lines = server.dispatched_total;
    ed_sha256(program, sizeof(program) - 1, digest);
    for (i = 0; i < sizeof(digest); i++) {
        static const char hex[] = "0123456789abcdef";
        digest_hex[i * 2] = hex[digest[i] >> 4];
        digest_hex[i * 2 + 1] = hex[digest[i] & 15u];
    }
    digest_hex[64] = '\0';
    strcpy(server.digest, digest_hex);
    if (ed_http_worker_client_init(&client, "https://cutter.test", "device-key",
                                   "laser-1", fake_http, random_bytes, &server) != ED_OK ||
        ed_http_worker_adapter_init(&client, &adapters.http) != ED_OK) return 0;
    server.client = &client;
    adapters.controller.context = &controller;
    adapters.controller.stream_verified = stream_verified;
    adapters.storage.context = &storage;
    adapters.storage.reset = storage_reset;
    adapters.storage.write = storage_write;
    adapters.storage.read = storage_read;
    adapters.storage.seal = storage_seal;
    adapters.storage.unseal = storage_unseal;
    adapters.receipt_store.context = &saved;
    adapters.receipt_store.load = receipt_store_load;
    adapters.receipt_store.save = receipt_store_save;
    adapters.receipt_store.clear = receipt_store_clear;

    first_status = ed_worker_run_one(&adapters, &result);
    if (!check(first_status == ED_ERR_REPORT && saved.present && saved.save_count == 1 &&
               server.lost_terminal_acks == 3 && server.terminal_applied &&
               server.terminal_application_count == 1 &&
               controller.stream_calls == 1 && controller.uart_size == sizeof(program) - 1,
               "all lost ACK attempts leave the exact terminal receipt in the durable outbox")) return 0;
    {
        char original_body[sizeof(server.first_terminal_body)];
        char original_session[ED_WORKER_SESSION_BYTES];
        strcpy(original_body, server.first_terminal_body);
        strcpy(original_session, saved.receipt.session);
        server.lose_terminal_acks = 0;
        retry_status = ed_worker_run_one(&adapters, &result);
        if (!check(retry_status == ED_OK && result.outcome == ED_WORKER_NO_ASSIGNMENT,
                   "saved receipt retries before polling again on the saved session")) return 0;
        if (!check(!saved.present && saved.clear_count == 1 &&
                   strcmp(original_body, server.last_terminal_body) == 0 &&
                   strcmp(original_session, server.first_session) == 0 &&
                   strcmp(server.session, server.first_session) == 0 &&
                   server.initial_poll_count == 2 && server.terminal_count == 4 &&
                   server.random_call_count == 1 && server.terminal_application_count == 1,
                   "retry preserves exact receipt/session, clears only on 200, and server applies once")) return 0;
        if (!check(controller.stream_calls == 1 && controller.uart_size == sizeof(program) - 1,
                   "receipt recovery never streams the completed assignment a second time")) return 0;
    }
    return !server.bad_request;
}

int main(void) {
    int ok = 1;
    ok &= check(run_scenario(RANGE_GOOD, 1, 0, 1),
                "full fake-server assignment, progress, and terminal receipt contract");
    ok &= check(run_scenario(RANGE_BAD_CONTENT_RANGE, 0, 0, 0),
                "malformed Content-Range produces no UART output");
    ok &= check(run_scenario(RANGE_TRUNCATED, 0, 0, 0),
                "truncated chunk produces no UART output");
    ok &= check(run_scenario(RANGE_BAD_DIGEST, 0, 0, 0),
                "digest mismatch produces no UART output");
    ok &= check(run_scenario(RANGE_GOOD, 0, 1, 2),
                "wrong active lease fails closed before completion");
    ok &= check(run_scenario(RANGE_GOOD, 0, 2, 2),
                "wrong active job fails closed before completion");
    ok &= check(run_scenario(RANGE_GOOD, 0, 3, 2),
                "unsupported active command fails closed before completion");
    ok &= check(test_durable_terminal_receipt_retry(),
                "durable receipt outbox recovers a lost terminal acknowledgment");
    return ok ? 0 : 1;
}

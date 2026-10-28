#include "http_client.h"

#include <stdio.h>
#include <string.h>

#define TEST_LEASE "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

_Static_assert(sizeof(TEST_LEASE) - 1 == 64, "test lease must match server token length");

typedef struct {
    unsigned char nonce[16];
    int random_calls;
    int request_calls;
    char method[8];
    char url[256];
    char body[256];
    char authorization[160];
    char content_type[64];
    char response[1024];
    int status;
    int follow_redirects;
} fixture;

static int random_bytes(void *context, unsigned char *bytes, size_t length) {
    fixture *f = (fixture *)context;
    f->random_calls++;
    if (length != sizeof(f->nonce)) return -1;
    memcpy(bytes, f->nonce, length);
    return 0;
}

static int request(void *context, const ed_http_request *input,
                   ed_http_response *output) {
    fixture *f = (fixture *)context;
    size_t i;
    f->request_calls++;
    f->follow_redirects = input->follow_redirects;
    if (input->body_length >= sizeof(f->body) ||
        input->url == NULL || strlen(input->url) >= sizeof(f->url)) return -1;
    strcpy(f->method, input->method);
    strcpy(f->url, input->url);
    memcpy(f->body, input->body, input->body_length);
    f->body[input->body_length] = '\0';
    for (i = 0; i < input->header_count; i++) {
        if (strcmp(input->headers[i].name, "Authorization") == 0)
            strcpy(f->authorization, input->headers[i].value);
        if (strcmp(input->headers[i].name, "Content-Type") == 0)
            strcpy(f->content_type, input->headers[i].value);
    }
    output->status = f->status;
    if (strlen(f->response) > output->body_capacity) return -1;
    memcpy(output->body, f->response, strlen(f->response));
    output->body_length = strlen(f->response);
    return 0;
}

static int check(int condition, const char *message) {
    if (condition) return 1;
    fprintf(stderr, "FAIL: %s\n", message);
    return 0;
}

int main(void) {
    fixture f = {0};
    ed_http_worker_client client;
    ed_http_worker_client invalid_client;
    ed_worker_assignment assignment;
    int has_assignment = -1;
    char session[ED_WORKER_SESSION_BYTES];
    int ok = 1;
    size_t i;
    char long_driver_id[66];
    char max_driver_id[65];

    for (i = 0; i < sizeof(f.nonce); i++) f.nonce[i] = (unsigned char)(i + 1);
    f.status = 200;
    strcpy(f.response, "{\"command\":\"idle\"}");
    ok &= check(ed_http_worker_client_init(&client, "https://cutter.test", "worker-secret",
                                          "laser-1", request, random_bytes, &f) == ED_OK,
                "client accepts a valid HTTPS endpoint and transport");
    memset(long_driver_id, 'd', sizeof(long_driver_id) - 1);
    long_driver_id[sizeof(long_driver_id) - 1] = '\0';
    ok &= check(ed_http_worker_client_init(&invalid_client, "https://cutter.test", "worker-secret",
                                          long_driver_id, request, random_bytes, &f) == ED_ERR_ARGUMENT,
                "driver ID longer than the server's 64-character limit is rejected");
    memset(max_driver_id, 'd', sizeof(max_driver_id) - 1);
    max_driver_id[sizeof(max_driver_id) - 1] = '\0';
    ok &= check(ed_http_worker_client_init(&invalid_client, "https://cutter.test", "worker-secret",
                                          max_driver_id, request, random_bytes, &f) == ED_OK,
                "driver ID at the server's 64-character limit is accepted");
    ok &= check(ed_http_worker_client_init(&invalid_client, "https://cutter.test", "worker-secret",
                                          "", request, random_bytes, &f) == ED_ERR_ARGUMENT,
                "empty driver ID is rejected");
    ok &= check(ed_http_worker_open_fresh_session(&client, session, sizeof(session)) == 0,
                "fresh session is generated");
    ok &= check(strcmp(session, "0102030405060708090a0b0c0d0e0f10") == 0 &&
                f.random_calls == 1, "session uses a fresh 128-bit nonce");
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) == 0,
                "initial poll accepts an idle response");
    ok &= check(f.request_calls == 1 && strcmp(f.method, "POST") == 0 &&
                strcmp(f.url, "https://cutter.test/api/v1/drivers/laser-1/poll") == 0,
                "poll uses the worker endpoint");
    ok &= check(strcmp(f.authorization, "Bearer worker-secret") == 0 &&
                strcmp(f.content_type, "application/json") == 0 &&
                f.follow_redirects == 0 &&
                strstr(f.body, "\"session\":\"0102030405060708090a0b0c0d0e0f10\"") != NULL &&
                strstr(f.body, "\"chunked_content_v1\"") != NULL,
                "poll authenticates, disables redirects, and advertises chunked transfer");
    ok &= check(has_assignment == 0, "idle response has no assignment");
    ok &= check(client.parser_workspace[0].end > client.parser_workspace[0].start,
                "JSON tokens are retained in client-owned parser workspace");
    {
        int previous_calls = f.request_calls;
        ok &= check(ed_http_worker_poll_assignment(&client, "bad\"session", &assignment,
                                                    &has_assignment) != 0 &&
                    f.request_calls == previous_calls,
                    "poll rejects a session that cannot be safely serialized");
    }

    strcpy(f.response,
           "{\"command\":\"run\",\"protocol\":2,\"lease\":\"" TEST_LEASE "\","
           "\"job\":{\"id\":\"0123456789abcdef0123456789abcdef\",\"total\":1,\"size\":4,"
           "\"content\":{\"size\":4,\"sha256\":\"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\"}}}");
    has_assignment = 0;
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) == 0,
                "poll accepts a protocol-2 chunked assignment");
    ok &= check(has_assignment == 1 && assignment.content.size == 4 &&
                strcmp(assignment.job_id, "0123456789abcdef0123456789abcdef") == 0 &&
                strcmp(assignment.lease, TEST_LEASE) == 0,
                "assignment exposes validated job, lease, and immutable manifest");

    strcpy(f.response,
           "{\"command\":\"run\",\"protocol\":1,\"lease\":\"" TEST_LEASE "\","
           "\"job\":{\"id\":\"0123456789abcdef0123456789abcdef\",\"gcode\":\"G1 X1\\n\"}}");
    has_assignment = 1;
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) != 0 && has_assignment == 0,
                "legacy whole-program assignments are rejected by the chunked adapter");

    strcpy(f.response, "{\"command\":\"idle\",}");
    has_assignment = 1;
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) != 0 && has_assignment == 0,
                "malformed JSON is rejected and output remains empty");
    strcpy(f.response,
           "{\"command\":\"run\",\"protocol\":2,\"protocol\":2,"
           "\"lease\":\"" TEST_LEASE "\",\"job\":{\"id\":\"0123456789abcdef0123456789abcdef\",\"total\":1,"
           "\"size\":4,\"content\":{\"size\":4,\"sha256\":"
           "\"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\"}}}");
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) != 0 && has_assignment == 0,
                "duplicate protocol fields are rejected");
    strcpy(f.response,
           "{\"command\":\"run\",\"protocol\":2,\"lease\":\"" TEST_LEASE "\","
           "\"job\":{\"id\":\"0123456789abcdef0123456789abcdef\",\"total\":1,\"size\":5,"
           "\"content\":{\"size\":4,\"sha256\":"
           "\"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\"}}}");
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) != 0 && has_assignment == 0,
                "assignment size must agree with the immutable content manifest");
    strcpy(f.response,
           "{\"command\":\"run\",\"protocol\":2,\"lease\":\"bad-lease\","
           "\"job\":{\"id\":\"0123456789abcdef0123456789abcdef\",\"total\":1,\"size\":4,"
           "\"content\":{\"size\":4,\"sha256\":"
           "\"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad\"}}}");
    ok &= check(ed_http_worker_poll_assignment(&client, session, &assignment,
                                               &has_assignment) != 0 && has_assignment == 0,
                "lease must be the server's 64-character lowercase hexadecimal token");
    return ok ? 0 : 1;
}

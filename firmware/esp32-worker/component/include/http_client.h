#ifndef EMBERDECK_HTTP_CLIENT_H
#define EMBERDECK_HTTP_CLIENT_H

#include "worker_cycle.h"

#define ED_HTTP_CLIENT_URL_BYTES 256u
#define ED_HTTP_CLIENT_KEY_BYTES 128u
#define ED_HTTP_CLIENT_DRIVER_ID_BYTES 65u
#define ED_HTTP_RESPONSE_BYTES 4096u
#define ED_HTTP_RESPONSE_HEADERS 8u
#define ED_HTTP_JSON_TOKEN_CAPACITY 160u
#define ED_HTTP_HEADER_NAME_BYTES 40u
#define ED_HTTP_HEADER_VALUE_BYTES 128u
#define ED_HTTP_REQUEST_BODY_BYTES 1024u

typedef struct {
    size_t start;
    size_t end;
    size_t next;
    unsigned char kind;
} ed_http_json_token;

typedef struct {
    const char *name;
    const char *value;
} ed_http_header;

typedef struct {
    char name[ED_HTTP_HEADER_NAME_BYTES];
    char value[ED_HTTP_HEADER_VALUE_BYTES];
} ed_http_response_header;

typedef struct {
    const char *method;
    const char *url;
    const ed_http_header *headers;
    size_t header_count;
    const unsigned char *body;
    size_t body_length;
    /* Authenticated worker requests must never follow redirects. */
    int follow_redirects;
} ed_http_request;

typedef struct {
    int status;
    unsigned char *body;
    size_t body_capacity;
    size_t body_length;
    ed_http_response_header headers[ED_HTTP_RESPONSE_HEADERS];
    size_t header_count;
} ed_http_response;

typedef int (*ed_http_transport)(void *context, const ed_http_request *request,
                                 ed_http_response *response);
typedef int (*ed_http_random_bytes)(void *context, unsigned char *bytes,
                                    size_t length);

typedef struct {
    char server_url[ED_HTTP_CLIENT_URL_BYTES];
    char worker_key[ED_HTTP_CLIENT_KEY_BYTES];
    char driver_id[ED_HTTP_CLIENT_DRIVER_ID_BYTES];
    ed_http_transport transport;
    ed_http_random_bytes random_bytes;
    void *context;
    /* Poll replies live in client-owned bounded workspace so the transport
     * does not need to allocate an unbounded response buffer. */
    unsigned char response_workspace[ED_HTTP_RESPONSE_BYTES];
    /* Header metadata is large enough to consume a small task's stack; keep
     * the complete transport response descriptor in the client workspace. */
    ed_http_response http_response_workspace;
    ed_http_json_token parser_workspace[ED_HTTP_JSON_TOKEN_CAPACITY];
    char active_session[ED_WORKER_SESSION_BYTES];
    char active_job_id[ED_WORKER_JOB_ID_BYTES];
    char active_lease[ED_WORKER_LEASE_BYTES];
    size_t active_content_size;
    char active_sha256[65];
    int has_active_assignment;
    char request_workspace[ED_HTTP_REQUEST_BODY_BYTES];
} ed_http_worker_client;

/* The transport owns TLS, sockets and response delivery. It must not follow
 * redirects because requests contain worker credentials. The
 * client sends only HTTPS requests and bounds the JSON poll response. */
ed_result ed_http_worker_client_init(ed_http_worker_client *client,
                                    const char *server_url,
                                    const char *worker_key,
                                    const char *driver_id,
                                    ed_http_transport transport,
                                    ed_http_random_bytes random_bytes,
                                    void *context);

/* Callback-compatible operations for ed_http_worker_adapter. A new random
 * 128-bit session is generated for every call. */
int ed_http_worker_open_fresh_session(void *context, char *session,
                                      size_t capacity);
int ed_http_worker_poll_assignment(void *context, const char *session,
                                   ed_worker_assignment *assignment,
                                   int *has_assignment);

/* Wires the client callbacks into the coordinator's HTTP adapter. The close
 * callback is local only; the server expires idle sessions by heartbeat TTL. */
ed_result ed_http_worker_adapter_init(ed_http_worker_client *client,
                                      ed_http_worker_adapter *adapter);

#endif

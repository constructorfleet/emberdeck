#include "http_client.h"

#include <ctype.h>
#include <stdio.h>
#include <string.h>

typedef enum { JSON_OBJECT, JSON_ARRAY, JSON_STRING, JSON_PRIMITIVE } json_kind;
typedef struct {
    const unsigned char *input;
    size_t length;
    size_t offset;
    ed_http_json_token *tokens;
    size_t count;
} json_parser;

static void skip_space(json_parser *parser) {
    while (parser->offset < parser->length &&
           (parser->input[parser->offset] == ' ' || parser->input[parser->offset] == '\t' ||
            parser->input[parser->offset] == '\r' || parser->input[parser->offset] == '\n'))
        parser->offset++;
}

static size_t add_token(json_parser *parser, json_kind kind, size_t start) {
    size_t index;
    if (parser->count >= ED_HTTP_JSON_TOKEN_CAPACITY) return ED_HTTP_JSON_TOKEN_CAPACITY;
    index = parser->count++;
    parser->tokens[index].kind = kind;
    parser->tokens[index].start = start;
    parser->tokens[index].end = start;
    parser->tokens[index].next = index + 1;
    return index;
}

static int valid_primitive(const unsigned char *input, size_t start, size_t end) {
    size_t i = start;
    if ((end - start == 4 && memcmp(input + start, "true", 4) == 0) ||
        (end - start == 5 && memcmp(input + start, "false", 5) == 0) ||
        (end - start == 4 && memcmp(input + start, "null", 4) == 0)) return 1;
    if (i < end && input[i] == '-') i++;
    if (i >= end) return 0;
    if (input[i] == '0') i++;
    else if (input[i] >= '1' && input[i] <= '9') {
        do { i++; } while (i < end && input[i] >= '0' && input[i] <= '9');
    } else return 0;
    if (i < end && input[i] == '.') {
        i++;
        if (i >= end || input[i] < '0' || input[i] > '9') return 0;
        do { i++; } while (i < end && input[i] >= '0' && input[i] <= '9');
    }
    if (i < end && (input[i] == 'e' || input[i] == 'E')) {
        i++;
        if (i < end && (input[i] == '+' || input[i] == '-')) i++;
        if (i >= end || input[i] < '0' || input[i] > '9') return 0;
        do { i++; } while (i < end && input[i] >= '0' && input[i] <= '9');
    }
    return i == end;
}

static int parse_value(json_parser *parser, unsigned depth, size_t *token_out);

static int parse_string(json_parser *parser, size_t *token_out) {
    size_t token, start;
    if (parser->offset >= parser->length || parser->input[parser->offset] != '"') return -1;
    start = ++parser->offset;
    token = add_token(parser, JSON_STRING, start);
    if (token == ED_HTTP_JSON_TOKEN_CAPACITY) return -1;
    while (parser->offset < parser->length) {
        unsigned char c = parser->input[parser->offset++];
        if (c == '"') {
            parser->tokens[token].end = parser->offset - 1;
            parser->tokens[token].next = parser->count;
            *token_out = token;
            return 0;
        }
        if (c < 0x20) return -1;
        if (c == '\\') {
            unsigned char escaped;
            if (parser->offset >= parser->length) return -1;
            escaped = parser->input[parser->offset++];
            if (escaped == 'u') {
                size_t i;
                for (i = 0; i < 4; i++) {
                    if (parser->offset >= parser->length ||
                        !isxdigit(parser->input[parser->offset++])) return -1;
                }
            } else if (escaped != '"' && escaped != '\\' && escaped != '/' &&
                       escaped != 'b' && escaped != 'f' && escaped != 'n' &&
                       escaped != 'r' && escaped != 't') return -1;
        }
    }
    return -1;
}

static int parse_value(json_parser *parser, unsigned depth, size_t *token_out) {
    size_t token;
    unsigned char c;
    if (depth > 8u) return -1;
    skip_space(parser);
    if (parser->offset >= parser->length) return -1;
    c = parser->input[parser->offset];
    if (c == '"') return parse_string(parser, token_out);
    if (c == '{' || c == '[') {
        unsigned char close = c == '{' ? '}' : ']';
        int object = c == '{';
        parser->offset++;
        token = add_token(parser, object ? JSON_OBJECT : JSON_ARRAY,
                          parser->offset - 1);
        if (token == ED_HTTP_JSON_TOKEN_CAPACITY) return -1;
        skip_space(parser);
        while (parser->offset < parser->length && parser->input[parser->offset] != close) {
            size_t child;
            if (object) {
                if (parse_string(parser, &child) != 0) return -1;
                skip_space(parser);
                if (parser->offset >= parser->length || parser->input[parser->offset++] != ':') return -1;
            }
            if (parse_value(parser, depth + 1u, &child) != 0) return -1;
            skip_space(parser);
            if (parser->offset < parser->length && parser->input[parser->offset] == ',') {
                parser->offset++;
                skip_space(parser);
                if (parser->offset < parser->length && parser->input[parser->offset] == close) return -1;
                continue;
            }
            if (parser->offset >= parser->length || parser->input[parser->offset] != close) return -1;
            break;
        }
        if (parser->offset >= parser->length || parser->input[parser->offset++] != close) return -1;
        parser->tokens[token].end = parser->offset;
        parser->tokens[token].next = parser->count;
        if (object) {
            size_t key = token + 1;
            while (key < parser->count) {
                size_t value = key + 1, previous = token + 1;
                size_t key_length = parser->tokens[key].end - parser->tokens[key].start;
                if (memchr(parser->input + parser->tokens[key].start, '\\', key_length) != NULL)
                    return -1;
                while (previous < key) {
                    size_t previous_value = previous + 1;
                    size_t previous_length = parser->tokens[previous].end - parser->tokens[previous].start;
                    if (previous_length == key_length &&
                        memcmp(parser->input + parser->tokens[previous].start,
                               parser->input + parser->tokens[key].start, key_length) == 0)
                        return -1;
                    previous = parser->tokens[previous_value].next;
                }
                key = parser->tokens[value].next;
            }
        }
        *token_out = token;
        return 0;
    }
    token = add_token(parser, JSON_PRIMITIVE, parser->offset);
    if (token == ED_HTTP_JSON_TOKEN_CAPACITY) return -1;
    while (parser->offset < parser->length && parser->input[parser->offset] != ',' &&
           parser->input[parser->offset] != '}' && parser->input[parser->offset] != ']' &&
           parser->input[parser->offset] != ' ' && parser->input[parser->offset] != '\t' &&
           parser->input[parser->offset] != '\r' && parser->input[parser->offset] != '\n')
        parser->offset++;
    if (parser->offset == parser->tokens[token].start) return -1;
    if (!valid_primitive(parser->input, parser->tokens[token].start, parser->offset)) return -1;
    parser->tokens[token].end = parser->offset;
    parser->tokens[token].next = parser->count;
    *token_out = token;
    return 0;
}

static int parse_document(json_parser *parser, size_t *root) {
    parser->offset = 0;
    parser->count = 0;
    if (parse_value(parser, 0, root) != 0) return -1;
    skip_space(parser);
    return parser->offset == parser->length ? 0 : -1;
}

/* Returns 1 for a unique field, 0 when absent, and -1 for duplicates. */
static int object_field(const json_parser *parser, size_t object, const char *name,
                        size_t *value_out) {
    size_t i, found = 0;
    size_t name_length = strlen(name);
    if (object >= parser->count || parser->tokens[object].kind != JSON_OBJECT) return -1;
    i = object + 1;
    while (i < parser->tokens[object].next) {
        size_t key = i, value = key + 1;
        if (key >= parser->count || value >= parser->count ||
            parser->tokens[key].kind != JSON_STRING) return -1;
        if (parser->tokens[key].end - parser->tokens[key].start == name_length &&
            memcmp(parser->input + parser->tokens[key].start, name, name_length) == 0) {
            if (found) return -1;
            found = 1;
            *value_out = value;
        }
        i = parser->tokens[value].next;
    }
    return found ? 1 : 0;
}

static int token_text(const json_parser *parser, size_t token, char *out,
                      size_t capacity) {
    size_t length;
    if (token >= parser->count || parser->tokens[token].kind != JSON_STRING) return -1;
    length = parser->tokens[token].end - parser->tokens[token].start;
    if (length == 0 || length >= capacity ||
        memchr(parser->input + parser->tokens[token].start, '\\', length) != NULL) return -1;
    memcpy(out, parser->input + parser->tokens[token].start, length);
    out[length] = '\0';
    return 0;
}

static int get_string(const json_parser *parser, size_t object, const char *key,
                      char *out, size_t capacity) {
    size_t value;
    int found = object_field(parser, object, key, &value);
    return found == 1 ? token_text(parser, value, out, capacity) : -1;
}

static int get_uint(const json_parser *parser, size_t object, const char *key,
                    size_t *out) {
    size_t value, i, number = 0;
    int found = object_field(parser, object, key, &value);
    if (found != 1 || parser->tokens[value].kind != JSON_PRIMITIVE ||
        parser->tokens[value].start == parser->tokens[value].end) return -1;
    if (parser->input[parser->tokens[value].start] == '0' &&
        parser->tokens[value].end - parser->tokens[value].start != 1) return -1;
    for (i = parser->tokens[value].start; i < parser->tokens[value].end; i++) {
        unsigned char c = parser->input[i];
        if (c < '0' || c > '9' || number > (SIZE_MAX - (size_t)(c - '0')) / 10u) return -1;
        number = number * 10u + (size_t)(c - '0');
    }
    *out = number;
    return 0;
}

static int valid_hex_id(const char *value) {
    size_t i;
    if (strlen(value) != 32) return 0;
    for (i = 0; i < 32; i++) if (!((value[i] >= '0' && value[i] <= '9') ||
                                    (value[i] >= 'a' && value[i] <= 'f'))) return 0;
    return 1;
}

static int valid_digest(const char *value, unsigned char digest[32]) {
    size_t i;
    if (strlen(value) != 64) return 0;
    for (i = 0; i < 32; i++) {
        char a = value[i * 2], b = value[i * 2 + 1];
        unsigned high, low;
        if (!((a >= '0' && a <= '9') || (a >= 'a' && a <= 'f')) ||
            !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f'))) return 0;
        high = a <= '9' ? (unsigned)(a - '0') : (unsigned)(a - 'a' + 10);
        low = b <= '9' ? (unsigned)(b - '0') : (unsigned)(b - 'a' + 10);
        digest[i] = (unsigned char)((high << 4) | low);
    }
    return 1;
}

static int valid_session(const char *value) {
    size_t i;
    if (strlen(value) != 32) return 0;
    for (i = 0; i < 32; i++) if (!((value[i] >= '0' && value[i] <= '9') ||
                                    (value[i] >= 'a' && value[i] <= 'f'))) return 0;
    return 1;
}

static int valid_lease(const char *value) {
    size_t i;
    if (strlen(value) != 64) return 0;
    for (i = 0; i < 64; i++) if (!((value[i] >= '0' && value[i] <= '9') ||
                                    (value[i] >= 'a' && value[i] <= 'f'))) return 0;
    return 1;
}

static int valid_driver_id(const char *value) {
    size_t i;
    size_t length = strlen(value);
    if (length == 0 || length > 64) return 0;
    for (i = 0; value[i] != '\0'; i++) {
        unsigned char c = (unsigned char)value[i];
        if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
              (c >= '0' && c <= '9') || c == '-' || c == '_')) return 0;
    }
    return 1;
}

static int get_object(const json_parser *parser, size_t parent, const char *key,
                      size_t *value_out) {
    int found = object_field(parser, parent, key, value_out);
    return found == 1 && parser->tokens[*value_out].kind == JSON_OBJECT ? 0 : -1;
}

static int ascii_case_equal(const char *left, const char *right) {
    while (*left != '\0' && *right != '\0') {
        unsigned char a = (unsigned char)*left++, b = (unsigned char)*right++;
        if (a >= 'A' && a <= 'Z') a = (unsigned char)(a - 'A' + 'a');
        if (b >= 'A' && b <= 'Z') b = (unsigned char)(b - 'A' + 'a');
        if (a != b) return 0;
    }
    return *left == '\0' && *right == '\0';
}

/* Returns 1 for one header, 0 when absent, and -1 for duplicates or invalid data. */
static int response_header(const ed_http_response *response, const char *name,
                           char *value, size_t capacity) {
    size_t i, matches = 0;
    if (response->header_count > ED_HTTP_RESPONSE_HEADERS) return -1;
    for (i = 0; i < response->header_count; i++) {
        const ed_http_response_header *header = &response->headers[i];
        if (memchr(header->name, '\0', sizeof(header->name)) == NULL) return -1;
        if (!ascii_case_equal(header->name, name)) continue;
        matches++;
        if (matches > 1 || header->name[0] == '\0' ||
            memchr(header->value, '\0', sizeof(header->value)) == NULL) return -1;
        if (strlen(header->value) >= capacity) return -1;
        memcpy(value, header->value, strlen(header->value) + 1);
    }
    return matches == 1 ? 1 : 0;
}

static int header_equals(const ed_http_response *response, const char *name,
                         const char *expected) {
    char value[ED_HTTP_HEADER_VALUE_BYTES];
    return response_header(response, name, value, sizeof(value)) == 1 &&
           strcmp(value, expected) == 0;
}

static int json_append_string(char *output, size_t capacity, size_t *used,
                              const char *value, size_t maximum) {
    static const char hex[] = "0123456789abcdef";
    size_t i;
    if (*used >= capacity || capacity - *used < 2) return -1;
    output[(*used)++] = '"';
    for (i = 0; i < maximum && value[i] != '\0'; i++) {
        unsigned char c = (unsigned char)value[i];
        if (c == '"' || c == '\\') {
            if (capacity - *used < 2) return -1;
            output[(*used)++] = '\\';
            output[(*used)++] = (char)c;
        } else if (c < 0x20) {
            if (capacity - *used < 6) return -1;
            output[(*used)++] = '\\'; output[(*used)++] = 'u';
            output[(*used)++] = '0'; output[(*used)++] = '0';
            output[(*used)++] = hex[c >> 4]; output[(*used)++] = hex[c & 15u];
        } else {
            if (*used >= capacity) return -1;
            output[(*used)++] = (char)c;
        }
    }
    if (i == maximum && value[i] != '\0') return -1;
    if (*used >= capacity) return -1;
    output[(*used)++] = '"';
    output[*used] = '\0';
    return 0;
}

static int build_poll_endpoint(const ed_http_worker_client *client, char *url,
                               size_t capacity, char *auth, size_t auth_capacity) {
    int amount = snprintf(url, capacity, "%s/api/v1/drivers/%s/poll",
                          client->server_url, client->driver_id);
    if (amount < 0 || (size_t)amount >= capacity) return -1;
    amount = snprintf(auth, auth_capacity, "Bearer %s", client->worker_key);
    return amount < 0 || (size_t)amount >= auth_capacity ? -1 : 0;
}

static int post_poll_json(ed_http_worker_client *client, const char *url,
                          const char *auth, const unsigned char *body,
                          size_t body_length, ed_http_response *response) {
    ed_http_header headers[2];
    ed_http_request request;
    request.method = "POST"; request.url = url; request.headers = headers;
    request.header_count = 2; request.body = body; request.body_length = body_length;
    request.follow_redirects = 0;
    headers[0].name = "Authorization"; headers[0].value = auth;
    headers[1].name = "Content-Type"; headers[1].value = "application/json";
    response->status = 0; response->body = client->response_workspace;
    response->body_capacity = sizeof(client->response_workspace);
    response->body_length = 0; response->header_count = 0;
    if (client->transport(client->context, &request, response) != 0 ||
        response->body != client->response_workspace ||
        response->body_capacity != sizeof(client->response_workspace) ||
        response->body_length > response->body_capacity ||
        response->header_count > ED_HTTP_RESPONSE_HEADERS) return -1;
    return 0;
}

static int assignment_matches(const ed_http_worker_client *client,
                              const char *session, const char *job_id,
                              const char *lease) {
    return client->has_active_assignment &&
           strcmp(client->active_session, session) == 0 &&
           strcmp(client->active_job_id, job_id) == 0 &&
           strcmp(client->active_lease, lease) == 0;
}

ed_result ed_http_worker_client_init(ed_http_worker_client *client,
                                    const char *server_url,
                                    const char *worker_key,
                                    const char *driver_id,
                                    ed_http_transport transport,
                                    ed_http_random_bytes random_bytes,
                                    void *context) {
    size_t url_length, key_length, driver_length, i;
    if (client == NULL || server_url == NULL || worker_key == NULL || driver_id == NULL ||
        transport == NULL || random_bytes == NULL) return ED_ERR_ARGUMENT;
    url_length = strlen(server_url); key_length = strlen(worker_key);
    driver_length = strlen(driver_id);
    if (url_length < 9 || strncmp(server_url, "https://", 8) != 0 ||
        url_length >= sizeof(client->server_url) ||
        key_length == 0 || key_length >= sizeof(client->worker_key) ||
        driver_length >= sizeof(client->driver_id) || !valid_driver_id(driver_id))
        return ED_ERR_ARGUMENT;
    for (i = 8; i < url_length; i++) {
        unsigned char c = (unsigned char)server_url[i];
        if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
              (c >= '0' && c <= '9') || c == '.' || c == '-' || c == ':' ||
              c == '[' || c == ']' || c == '%')) return ED_ERR_ARGUMENT;
    }
    for (i = 0; i < key_length; i++) {
        unsigned char c = (unsigned char)worker_key[i];
        if (c < 0x21 || c > 0x7e) return ED_ERR_ARGUMENT;
    }
    memset(client, 0, sizeof(*client));
    memcpy(client->server_url, server_url, url_length + 1);
    memcpy(client->worker_key, worker_key, key_length + 1);
    memcpy(client->driver_id, driver_id, driver_length + 1);
    client->transport = transport;
    client->random_bytes = random_bytes;
    client->context = context;
    return ED_OK;
}

int ed_http_worker_open_fresh_session(void *context, char *session, size_t capacity) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    unsigned char nonce[16];
    static const char hex[] = "0123456789abcdef";
    size_t i;
    if (session != NULL && capacity != 0) session[0] = '\0';
    if (client == NULL || session == NULL || capacity < 33 ||
        client->random_bytes == NULL ||
        client->random_bytes(client->context, nonce, sizeof(nonce)) != 0) return -1;
    for (i = 0; i < sizeof(nonce); i++) {
        session[i * 2] = hex[nonce[i] >> 4];
        session[i * 2 + 1] = hex[nonce[i] & 15u];
    }
    session[32] = '\0';
    return 0;
}

int ed_http_worker_poll_assignment(void *context, const char *session,
                                   ed_worker_assignment *assignment,
                                   int *has_assignment) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    char url[ED_HTTP_CLIENT_URL_BYTES], auth[ED_HTTP_CLIENT_KEY_BYTES + 8];
    char body[ED_WORKER_SESSION_BYTES + 80];
    ed_http_header headers[2];
    ed_http_request request;
    ed_http_response *response;
    json_parser parser;
    size_t root, protocol_token, job_token, content_token;
    size_t job_total, job_size, content_size;
    char command[16], job_id[ED_WORKER_JOB_ID_BYTES], lease[ED_WORKER_LEASE_BYTES];
    char digest_text[65];
    unsigned char digest[32];
    int amount;
    if (assignment == NULL || has_assignment == NULL) return -1;
    memset(assignment, 0, sizeof(*assignment));
    *has_assignment = 0;
    if (client == NULL || session == NULL || client->transport == NULL ||
        !valid_session(session)) return -1;
    client->has_active_assignment = 0;
    response = &client->http_response_workspace;
    amount = snprintf(url, sizeof(url), "%s/api/v1/drivers/%s/poll",
                      client->server_url, client->driver_id);
    if (amount < 0 || (size_t)amount >= sizeof(url)) return -1;
    amount = snprintf(auth, sizeof(auth), "Bearer %s", client->worker_key);
    if (amount < 0 || (size_t)amount >= sizeof(auth)) return -1;
    amount = snprintf(body, sizeof(body),
                      "{\"session\":\"%s\",\"capabilities\":[\"chunked_content_v1\"]}", session);
    if (amount < 0 || (size_t)amount >= sizeof(body)) return -1;
    headers[0].name = "Authorization"; headers[0].value = auth;
    headers[1].name = "Content-Type"; headers[1].value = "application/json";
    request.method = "POST"; request.url = url; request.headers = headers;
    request.header_count = 2; request.body = (const unsigned char *)body;
    request.body_length = (size_t)amount;
    request.follow_redirects = 0;
    response->status = 0; response->body = client->response_workspace;
    response->body_capacity = sizeof(client->response_workspace); response->body_length = 0;
    response->header_count = 0;
    if (client->transport(client->context, &request, response) != 0 ||
        response->status != 200 || response->body != client->response_workspace ||
        response->body_capacity != sizeof(client->response_workspace) ||
        response->body_length > response->body_capacity ||
        response->header_count > ED_HTTP_RESPONSE_HEADERS) return -1;
    /* Parse the delivered bytes only after transport bounds are checked. */
    parser.input = response->body;
    parser.length = response->body_length;
    parser.offset = 0;
    parser.tokens = client->parser_workspace;
    parser.count = 0;
    if (parse_document(&parser, &root) != 0 || parser.tokens[root].kind != JSON_OBJECT ||
        get_string(&parser, root, "command", command, sizeof(command)) != 0) return -1;
    if (strcmp(command, "idle") == 0) {
        *has_assignment = 0;
        return 0;
    }
    if (strcmp(command, "run") != 0 ||
        object_field(&parser, root, "protocol", &protocol_token) != 1 ||
        parser.tokens[protocol_token].kind != JSON_PRIMITIVE ||
        parser.tokens[protocol_token].end - parser.tokens[protocol_token].start != 1 ||
        parser.input[parser.tokens[protocol_token].start] != '2' ||
        get_string(&parser, root, "lease", lease, sizeof(lease)) != 0 ||
        get_object(&parser, root, "job", &job_token) != 0 ||
        get_string(&parser, job_token, "id", job_id, sizeof(job_id)) != 0 ||
        !valid_hex_id(job_id) || get_uint(&parser, job_token, "total", &job_total) != 0 ||
        get_uint(&parser, job_token, "size", &job_size) != 0 ||
        get_object(&parser, job_token, "content", &content_token) != 0 ||
        get_uint(&parser, content_token, "size", &content_size) != 0 ||
        get_string(&parser, content_token, "sha256", digest_text, sizeof(digest_text)) != 0 ||
        !valid_lease(lease) || job_total == 0 || job_total > content_size ||
        content_size == 0 || content_size > ED_MAX_PROGRAM_BYTES ||
        job_size != content_size || !valid_digest(digest_text, digest)) return -1;
    assignment->content.size = content_size;
    assignment->total = job_total;
    memcpy(assignment->content.sha256, digest, sizeof(digest));
    memcpy(assignment->job_id, job_id, strlen(job_id) + 1);
    memcpy(assignment->lease, lease, strlen(lease) + 1);
    memcpy(client->active_session, session, strlen(session) + 1);
    memcpy(client->active_job_id, job_id, strlen(job_id) + 1);
    memcpy(client->active_lease, lease, strlen(lease) + 1);
    client->active_content_size = content_size;
    memcpy(client->active_sha256, digest_text, sizeof(client->active_sha256));
    client->has_active_assignment = 1;
    *has_assignment = 1;
    return 0;
}

static size_t bounded_string_length(const char *value, size_t capacity) {
    size_t i;
    for (i = 0; i < capacity; i++) if (value[i] == '\0') return i;
    return capacity;
}

static int get_range_header(const ed_http_response *response, const char *name,
                            const char *expected) {
    return header_equals(response, name, expected);
}

static int fetch_range(void *context, const char *session, const char *job_id,
                       const char *lease, size_t offset, size_t max_length,
                       unsigned char *destination, size_t capacity,
                       size_t *received) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    char url[ED_HTTP_CLIENT_URL_BYTES], auth[ED_HTTP_CLIENT_KEY_BYTES + 8];
    char range[80], content_range[96], content_length[32];
    ed_http_header headers[4];
    ed_http_request request;
    ed_http_response *response;
    size_t length, end;
    int amount;
    if (received != NULL) *received = 0;
    if (client == NULL || session == NULL || job_id == NULL || lease == NULL ||
        destination == NULL || received == NULL || client->transport == NULL ||
        !assignment_matches(client, session, job_id, lease) || max_length == 0 ||
        max_length > ED_MAX_RANGE_BYTES || offset >= client->active_content_size ||
        capacity < max_length) return -1;
    length = client->active_content_size - offset;
    if (length > max_length) length = max_length;
    if (length > capacity || length == 0) return -1;
    response = &client->http_response_workspace;
    end = offset + length - 1;
    amount = snprintf(url, sizeof(url), "%s/api/v1/drivers/%s/jobs/%s/content",
                      client->server_url, client->driver_id, client->active_job_id);
    if (amount < 0 || (size_t)amount >= sizeof(url)) return -1;
    amount = snprintf(auth, sizeof(auth), "Bearer %s", client->worker_key);
    if (amount < 0 || (size_t)amount >= sizeof(auth)) return -1;
    amount = snprintf(range, sizeof(range), "bytes=%zu-%zu", offset, end);
    if (amount < 0 || (size_t)amount >= sizeof(range)) return -1;
    headers[0].name = "Authorization"; headers[0].value = auth;
    headers[1].name = "Range"; headers[1].value = range;
    headers[2].name = "X-EmberDeck-Session"; headers[2].value = client->active_session;
    headers[3].name = "X-EmberDeck-Lease"; headers[3].value = client->active_lease;
    request.method = "GET"; request.url = url; request.headers = headers;
    request.header_count = 4; request.body = NULL; request.body_length = 0;
    request.follow_redirects = 0;
    response->status = 0; response->body = destination;
    response->body_capacity = capacity; response->body_length = 0;
    response->header_count = 0;
    if (client->transport(client->context, &request, response) != 0 ||
        response->body != destination || response->body_capacity != capacity ||
        response->header_count > ED_HTTP_RESPONSE_HEADERS || response->status != 206 ||
        response->body_length != length) return -1;
    amount = snprintf(content_range, sizeof(content_range), "bytes %zu-%zu/%zu",
                      offset, end, client->active_content_size);
    if (amount < 0 || (size_t)amount >= sizeof(content_range)) return -1;
    amount = snprintf(content_length, sizeof(content_length), "%zu", length);
    if (amount < 0 || (size_t)amount >= sizeof(content_length) ||
        !get_range_header(response, "Content-Type", "application/octet-stream") ||
        !get_range_header(response, "Content-Range", content_range) ||
        !get_range_header(response, "Content-Length", content_length) ||
        !get_range_header(response, "X-EmberDeck-Content-SHA256", client->active_sha256))
        return -1;
    *received = length;
    return 0;
}

static int build_active_body(ed_http_worker_client *client, const char *session,
                             const char *job_id, const char *lease, size_t acked,
                             size_t total, const char *controller_state,
                             size_t state_length, size_t *body_length) {
    int amount;
    size_t used;
    amount = snprintf(client->request_workspace, sizeof(client->request_workspace),
                      "{\"session\":\"%s\",\"job_id\":\"%s\",\"lease\":\"%s\","
                      "\"state\":\"running\",\"acked\":%zu,\"total\":%zu,"
                      "\"controller_state\":", session, job_id, lease, acked, total);
    if (amount < 0 || (size_t)amount >= sizeof(client->request_workspace)) return -1;
    used = (size_t)amount;
    if (json_append_string(client->request_workspace, sizeof(client->request_workspace),
                           &used, controller_state, state_length) != 0 ||
        used + 2 > sizeof(client->request_workspace)) return -1;
    client->request_workspace[used++] = '}';
    client->request_workspace[used] = '\0';
    *body_length = used;
    return 0;
}

static int poll_active(void *context, const char *session, const char *job_id,
                       const char *lease, size_t acked, size_t total,
                       const char *controller_state, ed_active_response *active) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    char url[ED_HTTP_CLIENT_URL_BYTES], auth[ED_HTTP_CLIENT_KEY_BYTES + 8];
    char command[16], response_lease[ED_WORKER_LEASE_BYTES];
    char response_job_id[ED_WORKER_JOB_ID_BYTES];
    ed_http_response *response;
    json_parser parser;
    size_t state_length, body_length, root, job;
    if (active == NULL) return -1;
    memset(active, 0, sizeof(*active));
    if (client == NULL || session == NULL || job_id == NULL || lease == NULL ||
        controller_state == NULL || client->transport == NULL || total == 0 ||
        acked > total || !assignment_matches(client, session, job_id, lease)) return -1;
    response = &client->http_response_workspace;
    state_length = bounded_string_length(controller_state, ED_WORKER_CONTROLLER_STATE_BYTES);
    if (state_length == 0 || state_length >= ED_WORKER_CONTROLLER_STATE_BYTES ||
        build_active_body(client, session, job_id, lease, acked, total,
                          controller_state, state_length, &body_length) != 0 ||
        build_poll_endpoint(client, url, sizeof(url), auth, sizeof(auth)) != 0) return -1;
    if (post_poll_json(client, url, auth,
                       (const unsigned char *)client->request_workspace,
                       body_length, response) != 0 || response->status != 200) return -1;
    parser.input = response->body; parser.length = response->body_length;
    parser.offset = 0; parser.tokens = client->parser_workspace; parser.count = 0;
    if (parse_document(&parser, &root) != 0 || parser.tokens[root].kind != JSON_OBJECT ||
        get_string(&parser, root, "command", command, sizeof(command)) != 0 ||
        get_string(&parser, root, "lease", response_lease, sizeof(response_lease)) != 0 ||
        get_object(&parser, root, "job", &job) != 0 ||
        get_string(&parser, job, "id", response_job_id, sizeof(response_job_id)) != 0 ||
        strcmp(response_lease, lease) != 0 || strcmp(response_job_id, job_id) != 0)
        return -1;
    if (strcmp(command, "run") == 0) active->command = ED_ACTIVE_CONTINUE;
    else if (strcmp(command, "cancel") == 0) active->command = ED_ACTIVE_CANCEL;
    else return -1;
    memcpy(active->job_id, response_job_id, strlen(response_job_id) + 1);
    memcpy(active->lease, response_lease, strlen(response_lease) + 1);
    return 0;
}

static const char *terminal_state(ed_terminal_state state) {
    switch (state) {
    case ED_TERMINAL_COMPLETED: return "completed";
    case ED_TERMINAL_FAILED: return "failed";
    case ED_TERMINAL_CANCELED: return "canceled";
    default: return NULL;
    }
}

static int build_terminal_body(ed_http_worker_client *client, const char *session,
                               const char *job_id, const char *lease,
                               const ed_terminal_receipt *receipt,
                               size_t controller_length, size_t error_length,
                               size_t *body_length) {
    const char *state = terminal_state(receipt->state);
    size_t used;
    int amount;
    if (state == NULL) return -1;
    amount = snprintf(client->request_workspace, sizeof(client->request_workspace),
                      "{\"session\":\"%s\",\"job_id\":\"%s\",\"lease\":\"%s\","
                      "\"state\":\"%s\",\"acked\":%zu,\"total\":%zu,\"controller_state\":",
                      session, job_id, lease, state, receipt->acked, receipt->total);
    if (amount < 0 || (size_t)amount >= sizeof(client->request_workspace)) return -1;
    used = (size_t)amount;
    if (json_append_string(client->request_workspace, sizeof(client->request_workspace),
                           &used, receipt->controller_state, controller_length) != 0 ||
        used + sizeof(",\"error\":") > sizeof(client->request_workspace)) return -1;
    memcpy(client->request_workspace + used, ",\"error\":", sizeof(",\"error\":") - 1);
    used += sizeof(",\"error\":") - 1;
    if (json_append_string(client->request_workspace, sizeof(client->request_workspace),
                           &used, receipt->error, error_length) != 0 ||
        used + 2 > sizeof(client->request_workspace)) return -1;
    client->request_workspace[used++] = '}';
    client->request_workspace[used] = '\0';
    *body_length = used;
    return 0;
}

static int report_terminal(void *context, const char *session, const char *job_id,
                           const char *lease, const ed_terminal_receipt *receipt) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    char url[ED_HTTP_CLIENT_URL_BYTES], auth[ED_HTTP_CLIENT_KEY_BYTES + 8];
    size_t controller_length, error_length, body_length;
    ed_http_response *response;
    unsigned attempt;
    if (client == NULL || session == NULL || job_id == NULL || lease == NULL ||
        receipt == NULL || client->transport == NULL ||
        (client->has_active_assignment
             ? !assignment_matches(client, session, job_id, lease)
             : (!valid_session(session) || !valid_hex_id(job_id) || !valid_lease(lease))) ||
        receipt->acked > receipt->total || receipt->total == 0 ||
        terminal_state(receipt->state) == NULL) return -1;
    response = &client->http_response_workspace;
    controller_length = bounded_string_length(receipt->controller_state,
                                              sizeof(receipt->controller_state));
    error_length = bounded_string_length(receipt->error, sizeof(receipt->error));
    if (controller_length >= sizeof(receipt->controller_state) ||
        error_length >= sizeof(receipt->error) ||
        build_terminal_body(client, session, job_id, lease, receipt,
                            controller_length, error_length, &body_length) != 0 ||
        build_poll_endpoint(client, url, sizeof(url), auth, sizeof(auth)) != 0) return -1;
    for (attempt = 0; attempt < 3; attempt++) {
        if (post_poll_json(client, url, auth,
                           (const unsigned char *)client->request_workspace,
                           body_length, response) != 0) continue;
        if (response->status == 200) return 0;
        if (response->status < 500) return -1;
    }
    return -1;
}

static void close_session(void *context, const char *session) {
    ed_http_worker_client *client = (ed_http_worker_client *)context;
    if (client == NULL || session == NULL ||
        strcmp(client->active_session, session) != 0) return;
    client->has_active_assignment = 0;
    client->active_session[0] = '\0';
    client->active_job_id[0] = '\0';
    client->active_lease[0] = '\0';
    client->active_content_size = 0;
    client->active_sha256[0] = '\0';
}

ed_result ed_http_worker_adapter_init(ed_http_worker_client *client,
                                      ed_http_worker_adapter *adapter) {
    if (client == NULL || adapter == NULL || client->transport == NULL ||
        client->random_bytes == NULL) return ED_ERR_ARGUMENT;
    memset(adapter, 0, sizeof(*adapter));
    adapter->context = client;
    adapter->open_fresh_session = ed_http_worker_open_fresh_session;
    adapter->poll_assignment = ed_http_worker_poll_assignment;
    adapter->poll_active = poll_active;
    adapter->fetch_range = fetch_range;
    adapter->report_terminal = report_terminal;
    adapter->close_session = close_session;
    return ED_OK;
}

#include "worker_cycle.h"

#include <stdio.h>
#include <string.h>

typedef struct {
    const unsigned char *source;
    size_t source_size;
    unsigned char staged[64];
    size_t staged_size;
    unsigned char uart[64];
    size_t uart_size;
    size_t next_offset;
    int sealed;
    int no_assignment;
    int short_fetch;
    int incomplete_terminal;
    int fetch_rejected;
    int report_rejected;
    int open_calls;
    int poll_calls;
    int fetch_calls;
    int report_calls;
    int close_calls;
    int callback_error;
    ed_terminal_state reported_state;
    size_t reported_acked;
    size_t reported_total;
    char reported_error[ED_WORKER_ERROR_BYTES];
    char reported_controller_state[ED_WORKER_CONTROLLER_STATE_BYTES];
    char active_controller_state[ED_WORKER_CONTROLLER_STATE_BYTES];
    char session[ED_WORKER_SESSION_BYTES];
    const char *expected_lease;
    int active_calls;
    int active_fail_at;
    int wrong_response_at;
    int cancel_at;
    size_t last_acked;
    int saved_present;
    ed_worker_saved_receipt saved_receipt;
} fixture;

static int uart_sink(void *context, const unsigned char *bytes, size_t length);

static int poll_active(void *context, const char *session, const char *job_id,
                       const char *lease, size_t acked, size_t total,
                       const char *controller_state,
                       ed_active_response *response) {
    fixture *f = (fixture *)context;
    f->active_calls++;
    if (strcmp(session, f->session) != 0 || strcmp(job_id, "job-7") != 0 ||
        strcmp(lease, f->expected_lease) != 0 || acked < f->last_acked ||
        acked > total || strcmp(controller_state, f->active_controller_state) != 0) {
        f->callback_error = 1;
        return -1;
    }
    f->last_acked = acked;
    if (f->active_fail_at == f->active_calls) return -1;
    strcpy(response->job_id, "job-7");
    strcpy(response->lease, "lease-one");
    if (f->wrong_response_at == f->active_calls) strcpy(response->lease, "other-lease");
    response->command = f->cancel_at == f->active_calls ?
                        ED_ACTIVE_CANCEL : ED_ACTIVE_CONTINUE;
    return 0;
}

typedef struct {
    fixture *f;
    ed_active_poll active_poll;
    void *poll_context;
} stream_fixture;

static int controller_send(void *context, const unsigned char *bytes, size_t length) {
    stream_fixture *stream = (stream_fixture *)context;
    size_t i;
    for (i = 0; i < length; i++) {
        ed_active_command command;
        if (uart_sink(stream->f, bytes + i, 1) != 0) return -1;
        if (bytes[i] == '\n') {
            size_t acked = stream->f->last_acked + 1;
            if (stream->active_poll(stream->poll_context, acked, 2,
                                    stream->f->active_controller_state,
                                    &command) != 0) return -1;
            if (command == ED_ACTIVE_CANCEL) {
                stream->f->cancel_at = -1;
                stream->f->active_fail_at = -1;
                stream->f->reported_state = ED_TERMINAL_CANCELED;
                return -1;
            }
        }
    }
    return 0;
}

static int stream_verified(void *context, ed_verified_job *verified,
                           ed_active_poll active_poll, void *poll_context,
                           ed_terminal_receipt *receipt) {
    fixture *f = (fixture *)context;
    stream_fixture stream = {f, active_poll, poll_context};
    ed_result sent = ed_verified_send_uart(verified, controller_send, &stream);
    if (f->reported_state == ED_TERMINAL_CANCELED) {
        receipt->state = ED_TERMINAL_CANCELED;
        receipt->acked = f->last_acked;
        receipt->total = 2;
        strcpy(receipt->controller_state, "Idle");
        return 1;
    }
    if (sent != ED_OK) return -1;
    receipt->acked = f->incomplete_terminal ? 1u : 2u;
    receipt->state = ED_TERMINAL_COMPLETED;
    receipt->total = 2;
    receipt->controller_idle = f->incomplete_terminal ? 0 : 1;
    strcpy(receipt->controller_state, f->incomplete_terminal ? "Run" : "Idle");
    return 0;
}

static int open_session(void *context, char *session, size_t capacity) {
    fixture *f = (fixture *)context;
    f->open_calls++;
    if (capacity < sizeof("session-fresh-1")) return -1;
    strcpy(session, "session-fresh-1");
    strcpy(f->session, session);
    return 0;
}

static int poll_assignment(void *context, const char *session,
                           ed_worker_assignment *assignment,
                           int *has_assignment) {
    fixture *f = (fixture *)context;
    f->poll_calls++;
    if (strcmp(session, f->session) != 0) return -1;
    strcpy(assignment->job_id, "job-7");
    strcpy(assignment->lease, "lease-one");
    assignment->total = 2;
    assignment->content.size = f->source_size;
    ed_sha256(f->source, f->source_size, assignment->content.sha256);
    *has_assignment = f->no_assignment ? 0 : 1;
    return 0;
}

static int fetch_range(void *context, const char *session, const char *job_id,
                       const char *lease, size_t offset, size_t max_length,
                       unsigned char *destination, size_t capacity,
                       size_t *received) {
    fixture *f = (fixture *)context;
    size_t amount;
    f->fetch_calls++;
    if (strcmp(session, f->session) != 0 || strcmp(job_id, "job-7") != 0 ||
        strcmp(lease, f->expected_lease) != 0 ||
        offset != f->next_offset ||
        max_length > capacity || offset > f->source_size) {
        if (strcmp(lease, f->expected_lease) != 0) f->fetch_rejected = 1;
        else f->callback_error = 1;
        return -1;
    }
    amount = f->source_size - offset;
    if (amount > max_length) amount = max_length;
    if (f->short_fetch && amount > 0) amount--;
    memcpy(destination, f->source + offset, amount);
    *received = amount;
    f->next_offset += amount;
    return 0;
}

static int storage_reset(void *context, size_t expected_size) {
    fixture *f = (fixture *)context;
    if (expected_size > sizeof(f->staged)) return -1;
    f->staged_size = 0;
    return 0;
}

static int storage_write(void *context, size_t offset,
                         const unsigned char *bytes, size_t length) {
    fixture *f = (fixture *)context;
    if (f->sealed || offset + length > sizeof(f->staged)) return -1;
    memcpy(f->staged + offset, bytes, length);
    if (f->staged_size < offset + length) f->staged_size = offset + length;
    return 0;
}

static int storage_read(void *context, size_t offset, unsigned char *bytes,
                        size_t length) {
    fixture *f = (fixture *)context;
    if (offset + length > f->staged_size) return -1;
    memcpy(bytes, f->staged + offset, length);
    return 0;
}

static int storage_seal(void *context, size_t expected_size) {
    fixture *f = (fixture *)context;
    if (f->staged_size != expected_size) return -1;
    f->sealed = 1;
    return 0;
}

static void storage_unseal(void *context) {
    ((fixture *)context)->sealed = 0;
}

static int uart_sink(void *context, const unsigned char *bytes, size_t length) {
    fixture *f = (fixture *)context;
    if (!f->sealed || f->uart_size + length > sizeof(f->uart)) return -1;
    memcpy(f->uart + f->uart_size, bytes, length);
    f->uart_size += length;
    return 0;
}

static int report_terminal(void *context, const char *session,
                           const char *job_id, const char *lease,
                           const ed_terminal_receipt *receipt) {
    fixture *f = (fixture *)context;
    f->report_calls++;
    f->reported_state = receipt->state;
    f->reported_acked = receipt->acked;
    f->reported_total = receipt->total;
    strcpy(f->reported_controller_state, receipt->controller_state);
    if (strcmp(session, f->session) != 0 || strcmp(job_id, "job-7") != 0 ||
        strcmp(lease, f->expected_lease) != 0) {
        if (strcmp(lease, f->expected_lease) != 0) f->report_rejected = 1;
        else f->callback_error = 1;
        return -1;
    }
    strcpy(f->reported_error, receipt->error);
    if (receipt->state == ED_TERMINAL_COMPLETED &&
        (receipt->acked != 2 || receipt->total != 2 || !receipt->controller_idle ||
         strcmp(receipt->controller_state, "Idle") != 0)) {
        f->callback_error = 1;
        return -1;
    }
    if (receipt->state == ED_TERMINAL_FAILED &&
        (receipt->acked > receipt->total || receipt->error[0] == '\0')) {
        f->callback_error = 1;
        return -1;
    }
    if (receipt->state != ED_TERMINAL_COMPLETED &&
        receipt->state != ED_TERMINAL_FAILED &&
        receipt->state != ED_TERMINAL_CANCELED) {
        f->callback_error = 1;
        return -1;
    }
    return 0;
}

static void close_session(void *context, const char *session) {
    fixture *f = (fixture *)context;
    if (strcmp(session, f->session) != 0) f->callback_error = 1;
    f->close_calls++;
}

static int check(int condition, const char *message) {
    if (!condition) fprintf(stderr, "FAIL: %s\n", message);
    return condition;
}

static int receipt_load(void *context, ed_worker_saved_receipt *receipt,
                        int *present) {
    fixture *f = (fixture *)context;
    *present = f->saved_present;
    if (f->saved_present) *receipt = f->saved_receipt;
    return 0;
}

static int receipt_save(void *context, const ed_worker_saved_receipt *receipt) {
    fixture *f = (fixture *)context;
    f->saved_receipt = *receipt;
    f->saved_present = 1;
    return 0;
}

static int receipt_clear(void *context) {
    ((fixture *)context)->saved_present = 0;
    return 0;
}

static void prepare(fixture *f, ed_worker_adapters *adapters,
                    const unsigned char *program, size_t length) {
    memset(f, 0, sizeof(*f));
    memset(adapters, 0, sizeof(*adapters));
    f->source = program;
    f->source_size = length;
    f->expected_lease = "lease-one";
    strcpy(f->active_controller_state, "Run");
    adapters->http.context = f;
    adapters->http.open_fresh_session = open_session;
    adapters->http.poll_assignment = poll_assignment;
    adapters->http.poll_active = poll_active;
    adapters->http.fetch_range = fetch_range;
    adapters->http.report_terminal = report_terminal;
    adapters->http.close_session = close_session;
    adapters->controller.context = f;
    adapters->controller.stream_verified = stream_verified;
    adapters->storage.context = f;
    adapters->storage.reset = storage_reset;
    adapters->storage.write = storage_write;
    adapters->storage.read = storage_read;
    adapters->storage.seal = storage_seal;
    adapters->storage.unseal = storage_unseal;
    adapters->receipt_store.context = f;
    adapters->receipt_store.load = receipt_load;
    adapters->receipt_store.save = receipt_save;
    adapters->receipt_store.clear = receipt_clear;
}

static int test_successful_assignment(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f = {0};
    ed_worker_adapters adapters = {0};
    ed_worker_result result;
    ed_result status;

    prepare(&f, &adapters, program, sizeof(program) - 1);

    status = ed_worker_run_one(&adapters, &result);
    if (!check(status == ED_OK, "one valid assignment completes")) return 0;
    if (!check(result.outcome == ED_WORKER_COMPLETED && result.acked == 2 &&
               result.total == 2, "terminal result is returned")) return 0;
    if (!check(f.open_calls == 1 && f.poll_calls == 1 && f.active_calls == 2 &&
               f.last_acked == 2,
               "repeated active polls report monotonic acknowledged progress")) return 0;
    if (!check(f.fetch_calls == 1 && f.next_offset == f.source_size,
               "content is fetched under the one assignment")) return 0;
    if (!check(f.uart_size == f.source_size && f.report_calls == 1,
               "verified content reaches UART before report")) return 0;
    if (!check(f.close_calls == 1 && !f.callback_error,
               "session closes after terminal report")) return 0;
    return 1;
}

static int test_no_assignment_closes_session(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.no_assignment = 1;
    if (!check(ed_worker_run_one(&adapters, &result) == ED_OK,
               "empty poll is a normal one-cycle result")) return 0;
    return check(result.outcome == ED_WORKER_NO_ASSIGNMENT &&
                 f.open_calls == 1 && f.poll_calls == 1 && f.close_calls == 1 &&
                 f.fetch_calls == 0 && f.uart_size == 0 && f.report_calls == 0,
                 "no assignment closes its fresh session without side effects");
}

static int test_wrong_lease_is_rejected(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.expected_lease = "different-lease";
    if (!check(ed_worker_run_one(&adapters, &result) == ED_ERR_REPORT,
               "wrong lease prevents the failure receipt from releasing it")) return 0;
    return check(f.fetch_calls == 1 && f.uart_size == 0 &&
                 f.report_calls == 1 && f.reported_state == ED_TERMINAL_FAILED &&
                 f.fetch_rejected && f.report_rejected &&
                 f.close_calls == 1,
                 "wrong lease never reaches controller and cannot claim terminal ownership");
}

static int test_invalid_content_never_reaches_uart_or_completion(void) {
    static const unsigned char malformed[] = "G1 X1\n(unterminated\n";
    static const unsigned char valid[] = "G1 X1\nM5\n";
    const unsigned char *programs[] = {malformed, valid};
    const size_t lengths[] = {sizeof(malformed) - 1, sizeof(valid) - 1};
    const int short_fetch[] = {0, 1};
    const ed_result expected[] = {ED_ERR_GCODE, ED_ERR_SIZE};
    const char *names[] = {"malformed G-code", "truncated content"};
    size_t i;
    for (i = 0; i < sizeof(programs) / sizeof(programs[0]); i++) {
        fixture f;
        ed_worker_adapters adapters;
        ed_worker_result result;
        prepare(&f, &adapters, programs[i], lengths[i]);
        f.short_fetch = short_fetch[i];
        if (!check(ed_worker_run_one(&adapters, &result) == expected[i], names[i])) return 0;
        if (!check(f.uart_size == 0 && f.report_calls == 1 &&
                   f.reported_state == ED_TERMINAL_FAILED && f.close_calls == 1,
                   "invalid or truncated content cannot reach UART/completed report")) return 0;
    }
    return 1;
}

static int test_incomplete_controller_result_is_not_reported_completed(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.incomplete_terminal = 1;
    if (!check(ed_worker_run_one(&adapters, &result) == ED_ERR_PROTOCOL,
               "completed requires all acknowledgments and controller Idle")) return 0;
    return check(f.uart_size == f.source_size &&
                 f.report_calls == 1 && f.reported_state == ED_TERMINAL_FAILED &&
                 f.reported_acked == 2 && f.reported_total == 2 &&
                 strcmp(f.reported_controller_state, "Run") == 0 &&
                 f.close_calls == 1 &&
                 result.outcome == ED_WORKER_ERROR,
                 "incomplete controller result is not reported as terminal");
}

static int test_active_cancel_stops_stream_and_reports_canceled(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.cancel_at = 1;
    if (!check(ed_worker_run_one(&adapters, &result) == ED_OK,
               "active cancellation is a successful terminal cycle")) return 0;
    return check(result.outcome == ED_WORKER_CANCELED && f.active_calls == 1 &&
                 f.uart_size == 6 && f.report_calls == 1 &&
                 f.reported_state == ED_TERMINAL_CANCELED && f.close_calls == 1,
                 "cancel stops later output, reports canceled, and does not replay");
}

static int test_active_poll_failure_stops_stream_and_reports_failure(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.active_fail_at = 1;
    strcpy(f.active_controller_state, "Hold");
    if (!check(ed_worker_run_one(&adapters, &result) == ED_ERR_SESSION,
               "failed active poll halts the claimed lease")) return 0;
    return check(f.active_calls == 1 && f.uart_size == 6 && f.report_calls == 1 &&
                 f.reported_state == ED_TERMINAL_FAILED && f.close_calls == 1 &&
                 f.open_calls == 1 && f.fetch_calls == 1 &&
                 strcmp(f.reported_controller_state, "Hold") == 0,
                 "poll failure halts output, preserves state, and does not replay");
}

static int test_active_poll_identity_change_stops_stream(void) {
    static const unsigned char program[] = "G1 X1\nM5\n";
    fixture f;
    ed_worker_adapters adapters;
    ed_worker_result result;
    prepare(&f, &adapters, program, sizeof(program) - 1);
    f.wrong_response_at = 1;
    if (!check(ed_worker_run_one(&adapters, &result) == ED_ERR_SESSION,
               "changed lease in active response is rejected")) return 0;
    return check(f.active_calls == 1 && f.uart_size == 6 && f.report_calls == 1 &&
                 f.reported_state == ED_TERMINAL_FAILED && f.close_calls == 1,
                 "identity change stops output and does not replay assignment");
}

int main(void) {
    if (!test_successful_assignment()) return 1;
    if (!test_no_assignment_closes_session()) return 1;
    if (!test_wrong_lease_is_rejected()) return 1;
    if (!test_invalid_content_never_reaches_uart_or_completion()) return 1;
    if (!test_incomplete_controller_result_is_not_reported_completed()) return 1;
    if (!test_active_cancel_stops_stream_and_reports_canceled()) return 1;
    if (!test_active_poll_failure_stops_stream_and_reports_failure()) return 1;
    if (!test_active_poll_identity_change_stops_stream()) return 1;
    return 0;
}

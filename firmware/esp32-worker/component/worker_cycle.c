#include "worker_cycle.h"

#include <string.h>

typedef struct {
    const ed_http_worker_adapter *http;
    const char *session;
    const ed_worker_assignment *assignment;
} range_context;

static int terminated(const char *text, size_t capacity) {
    return text != NULL && memchr(text, '\0', capacity) != NULL;
}

static int fetch_assignment_range(void *context, size_t offset,
                                  size_t max_length,
                                  unsigned char *destination,
                                  size_t capacity, size_t *received) {
    range_context *range = (range_context *)context;
    return range->http->fetch_range(
        range->http->context, range->session, range->assignment->job_id,
        range->assignment->lease, offset, max_length, destination, capacity,
        received);
}

static void close_session(const ed_http_worker_adapter *http,
                          const char *session) {
    http->close_session(http->context, session);
}

static ed_result receipt_outcome(ed_terminal_state state,
                                 ed_worker_outcome *outcome) {
    switch (state) {
    case ED_TERMINAL_COMPLETED:
        *outcome = ED_WORKER_COMPLETED;
        return ED_OK;
    case ED_TERMINAL_FAILED:
        *outcome = ED_WORKER_FAILED;
        return ED_OK;
    case ED_TERMINAL_CANCELED:
        *outcome = ED_WORKER_CANCELED;
        return ED_OK;
    default:
        return ED_ERR_PROTOCOL;
    }
}

static const char *failure_message(ed_result status) {
    switch (status) {
    case ED_ERR_FETCH: return "content range fetch failed";
    case ED_ERR_TOO_LARGE: return "program exceeds device size limit";
    case ED_ERR_SIZE: return "content range was truncated";
    case ED_ERR_DIGEST: return "content digest verification failed";
    case ED_ERR_GCODE: return "program G-code validation failed";
    case ED_ERR_STORAGE: return "staging storage failed";
    case ED_ERR_MEMORY: return "could not allocate verified job";
    case ED_ERR_UART: return "UART stream failed";
    case ED_ERR_CONTROLLER: return "controller terminal state was not confirmed";
    case ED_ERR_PROTOCOL: return "controller returned an invalid terminal receipt";
    default: return "worker assignment failed before verified completion";
    }
}

static ed_result report_failed_assignment(const ed_http_worker_adapter *http,
                                         const char *session,
                                         const ed_worker_assignment *assignment,
                                         ed_result cause) {
    ed_terminal_receipt receipt = {0};
    receipt.state = ED_TERMINAL_FAILED;
    (void)strncpy(receipt.error, failure_message(cause),
                  sizeof(receipt.error) - 1);
    if (http->report_terminal(http->context, session, assignment->job_id,
                              assignment->lease, &receipt) != 0)
        return ED_ERR_REPORT;
    return cause;
}

ed_result ed_worker_run_one(const ed_worker_adapters *adapters,
                            ed_worker_result *result) {
    char session[ED_WORKER_SESSION_BYTES] = {0};
    ed_worker_assignment assignment = {0};
    ed_terminal_receipt receipt = {0};
    ed_verified_job *verified = NULL;
    ed_worker_outcome outcome;
    range_context range;
    ed_result status;
    int has_assignment = 0;
    int session_open = 0;

    if (result == NULL) return ED_ERR_ARGUMENT;
    memset(result, 0, sizeof(*result));
    result->outcome = ED_WORKER_ERROR;
    if (adapters == NULL || adapters->http.open_fresh_session == NULL ||
        adapters->http.poll_assignment == NULL ||
        adapters->http.fetch_range == NULL ||
        adapters->http.report_terminal == NULL ||
        adapters->http.close_session == NULL ||
        adapters->controller.wait_terminal == NULL ||
        adapters->storage.reset == NULL || adapters->storage.write == NULL ||
        adapters->storage.read == NULL || adapters->storage.seal == NULL ||
        adapters->storage.unseal == NULL || adapters->uart_sink == NULL)
        return ED_ERR_ARGUMENT;

    if (adapters->http.open_fresh_session(adapters->http.context, session,
                                          sizeof(session)) != 0)
        return ED_ERR_SESSION;
    session_open = 1;
    if (!terminated(session, sizeof(session)) || session[0] == '\0') {
        session[0] = '\0';
        status = ED_ERR_PROTOCOL;
        goto done;
    }

    if (adapters->http.poll_assignment(adapters->http.context, session,
                                       &assignment, &has_assignment) != 0) {
        status = ED_ERR_SESSION;
        goto done;
    }
    if (has_assignment != 0 && has_assignment != 1) {
        status = ED_ERR_PROTOCOL;
        goto done;
    }
    if (!has_assignment) {
        result->outcome = ED_WORKER_NO_ASSIGNMENT;
        status = ED_OK;
        goto done;
    }
    if (!terminated(assignment.job_id, sizeof(assignment.job_id)) ||
        assignment.job_id[0] == '\0' ||
        !terminated(assignment.lease, sizeof(assignment.lease)) ||
        assignment.lease[0] == '\0') {
        status = ED_ERR_PROTOCOL;
        goto done;
    }

    range.http = &adapters->http;
    range.session = session;
    range.assignment = &assignment;
    status = ed_stage_verify(&assignment.content, fetch_assignment_range,
                             &range, &adapters->storage, &verified);
    if (status != ED_OK) {
        status = report_failed_assignment(&adapters->http, session,
                                          &assignment, status);
        goto done;
    }

    status = ed_verified_send_uart(verified, adapters->uart_sink,
                                   adapters->uart_context);
    ed_verified_release(verified);
    verified = NULL;
    if (status != ED_OK) {
        status = report_failed_assignment(&adapters->http, session,
                                          &assignment, status);
        goto done;
    }

    if (adapters->controller.wait_terminal(adapters->controller.context,
                                           &receipt) != 0) {
        status = report_failed_assignment(&adapters->http, session,
                                          &assignment, ED_ERR_CONTROLLER);
        goto done;
    }
    status = receipt_outcome(receipt.state, &outcome);
    if (status != ED_OK ||
        !terminated(receipt.controller_state, sizeof(receipt.controller_state)) ||
        !terminated(receipt.error, sizeof(receipt.error)) ||
        receipt.acked > receipt.total ||
        receipt.total == 0 ||
        (receipt.state == ED_TERMINAL_COMPLETED &&
         (receipt.acked != receipt.total || !receipt.controller_idle ||
          strcmp(receipt.controller_state, "Idle") != 0 ||
          receipt.error[0] != '\0'))) {
        status = report_failed_assignment(&adapters->http, session,
                                          &assignment, ED_ERR_PROTOCOL);
        goto done;
    }
    if (adapters->http.report_terminal(adapters->http.context, session,
                                       assignment.job_id, assignment.lease,
                                       &receipt) != 0) {
        status = ED_ERR_REPORT;
        goto done;
    }
    result->outcome = outcome;
    result->acked = receipt.acked;
    result->total = receipt.total;
    status = ED_OK;

done:
    if (verified != NULL) ed_verified_release(verified);
    if (session_open) close_session(&adapters->http, session);
    return status;
}

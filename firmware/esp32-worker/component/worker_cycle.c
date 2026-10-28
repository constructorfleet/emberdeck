#include "worker_cycle.h"

#include <string.h>

typedef struct {
    const ed_http_worker_adapter *http;
    const char *session;
    const ed_worker_assignment *assignment;
} range_context;

typedef struct {
    const ed_http_worker_adapter *http;
    const char *session;
    const ed_worker_assignment *assignment;
    size_t acked;
    size_t total;
    int have_progress;
    int failed;
    char controller_state[ED_WORKER_CONTROLLER_STATE_BYTES];
} active_context;

static int terminated(const char *text, size_t capacity);

static int poll_active(void *context, size_t acked, size_t total,
                       const char *controller_state,
                       ed_active_command *command) {
    active_context *active = (active_context *)context;
    ed_active_response response = {0};
    if (active == NULL || command == NULL || controller_state == NULL ||
        !terminated(controller_state, ED_WORKER_CONTROLLER_STATE_BYTES) ||
        controller_state[0] == '\0' || total == 0 || acked > total ||
        (active->have_progress &&
         (acked < active->acked || total != active->total))) {
        if (active != NULL) active->failed = 1;
        return -1;
    }
    response.command = ED_ACTIVE_CONTINUE;
    /* Retain physical controller progress even if the HTTP exchange fails. */
    active->acked = acked;
    active->total = total;
    active->have_progress = 1;
    (void)strncpy(active->controller_state, controller_state,
                  sizeof(active->controller_state) - 1);
    if (active->http->poll_active(active->http->context, active->session,
                                  active->assignment->job_id,
                                  active->assignment->lease, acked, total,
                                  controller_state, &response) != 0 ||
        !terminated(response.job_id, sizeof(response.job_id)) ||
        !terminated(response.lease, sizeof(response.lease)) ||
        strcmp(response.job_id, active->assignment->job_id) != 0 ||
        strcmp(response.lease, active->assignment->lease) != 0 ||
        (response.command != ED_ACTIVE_CONTINUE &&
         response.command != ED_ACTIVE_CANCEL)) {
        active->failed = 1;
        return -1;
    }
    *command = response.command;
    return 0;
}

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
                                         const ed_worker_receipt_store *store,
                                         const char *session,
                                         const ed_worker_assignment *assignment,
                                         ed_result cause) {
    ed_terminal_receipt receipt = {0};
    receipt.state = ED_TERMINAL_FAILED;
    receipt.total = assignment->total;
    (void)strncpy(receipt.error, failure_message(cause),
                  sizeof(receipt.error) - 1);
    {
        ed_worker_saved_receipt saved = {0};
        memcpy(saved.session, session, strlen(session) + 1);
        memcpy(saved.job_id, assignment->job_id, strlen(assignment->job_id) + 1);
        memcpy(saved.lease, assignment->lease, strlen(assignment->lease) + 1);
        saved.receipt = receipt;
        if (store->save(store->context, &saved) != 0) return ED_ERR_STORAGE;
        if (http->report_terminal(http->context, session, assignment->job_id,
                                  assignment->lease, &receipt) != 0)
            return ED_ERR_REPORT;
        if (store->clear(store->context) != 0) return ED_ERR_STORAGE;
    }
    return cause;
}

static ed_result report_failed_progress(const ed_http_worker_adapter *http,
                                        const ed_worker_receipt_store *store,
                                        const char *session,
                                        const ed_worker_assignment *assignment,
                                        ed_result cause,
                                        const active_context *active) {
    ed_terminal_receipt receipt = {0};
    receipt.state = ED_TERMINAL_FAILED;
    receipt.total = assignment->total;
    if (active != NULL && active->have_progress) {
        receipt.acked = active->acked;
        receipt.total = active->total;
        (void)strncpy(receipt.controller_state, active->controller_state,
                      sizeof(receipt.controller_state) - 1);
    }
    (void)strncpy(receipt.error, failure_message(cause), sizeof(receipt.error) - 1);
    {
        ed_worker_saved_receipt saved = {0};
        memcpy(saved.session, session, strlen(session) + 1);
        memcpy(saved.job_id, assignment->job_id, strlen(assignment->job_id) + 1);
        memcpy(saved.lease, assignment->lease, strlen(assignment->lease) + 1);
        saved.receipt = receipt;
        if (store->save(store->context, &saved) != 0) return ED_ERR_STORAGE;
        if (http->report_terminal(http->context, session, assignment->job_id,
                                  assignment->lease, &receipt) != 0)
            return ED_ERR_REPORT;
        if (store->clear(store->context) != 0) return ED_ERR_STORAGE;
    }
    return cause;
}

static int valid_saved_receipt(const ed_worker_saved_receipt *saved) {
    return terminated(saved->session, sizeof(saved->session)) && saved->session[0] != '\0' &&
           terminated(saved->job_id, sizeof(saved->job_id)) && saved->job_id[0] != '\0' &&
           terminated(saved->lease, sizeof(saved->lease)) && saved->lease[0] != '\0' &&
           terminated(saved->receipt.controller_state,
                      sizeof(saved->receipt.controller_state)) &&
           terminated(saved->receipt.error, sizeof(saved->receipt.error)) &&
           saved->receipt.total != 0 &&
           saved->receipt.acked <= saved->receipt.total &&
           (saved->receipt.state == ED_TERMINAL_COMPLETED ||
            saved->receipt.state == ED_TERMINAL_FAILED ||
            saved->receipt.state == ED_TERMINAL_CANCELED);
}

static ed_result save_and_report(const ed_http_worker_adapter *http,
                                 const ed_worker_receipt_store *store,
                                 const char *session, const char *job_id,
                                 const char *lease,
                                 const ed_terminal_receipt *receipt) {
    ed_worker_saved_receipt saved = {0};
    memcpy(saved.session, session, strlen(session) + 1);
    memcpy(saved.job_id, job_id, strlen(job_id) + 1);
    memcpy(saved.lease, lease, strlen(lease) + 1);
    saved.receipt = *receipt;
    if (store->save(store->context, &saved) != 0) return ED_ERR_STORAGE;
    if (http->report_terminal(http->context, session, job_id, lease, receipt) != 0)
        return ED_ERR_REPORT;
    return store->clear(store->context) == 0 ? ED_OK : ED_ERR_STORAGE;
}

ed_result ed_worker_run_one(const ed_worker_adapters *adapters,
                            ed_worker_result *result) {
    char session[ED_WORKER_SESSION_BYTES] = {0};
    ed_worker_assignment assignment = {0};
    ed_terminal_receipt receipt = {0};
    ed_verified_job *verified = NULL;
    ed_worker_outcome outcome;
    range_context range;
    active_context active;
    ed_result status;
    int has_assignment = 0;
    int session_open = 0;
    int has_saved_receipt = 0;
    ed_worker_saved_receipt saved_receipt = {0};

    if (result == NULL) return ED_ERR_ARGUMENT;
    memset(result, 0, sizeof(*result));
    result->outcome = ED_WORKER_ERROR;
    if (adapters == NULL || adapters->http.open_fresh_session == NULL ||
        adapters->http.poll_assignment == NULL ||
        adapters->http.fetch_range == NULL ||
        adapters->http.report_terminal == NULL ||
        adapters->http.close_session == NULL ||
        adapters->receipt_store.load == NULL || adapters->receipt_store.save == NULL ||
        adapters->receipt_store.clear == NULL ||
        adapters->http.poll_active == NULL ||
        adapters->controller.stream_verified == NULL ||
        adapters->storage.reset == NULL || adapters->storage.write == NULL ||
        adapters->storage.read == NULL || adapters->storage.seal == NULL ||
        adapters->storage.unseal == NULL)
        return ED_ERR_ARGUMENT;

    if (adapters->receipt_store.load(adapters->receipt_store.context,
                                     &saved_receipt, &has_saved_receipt) != 0 ||
        (has_saved_receipt != 0 && has_saved_receipt != 1)) return ED_ERR_STORAGE;
    if (has_saved_receipt) {
        if (!valid_saved_receipt(&saved_receipt)) return ED_ERR_PROTOCOL;
        memcpy(session, saved_receipt.session, strlen(saved_receipt.session) + 1);
        session_open = 1;
        status = adapters->http.report_terminal(adapters->http.context,
                                                saved_receipt.session,
                                                saved_receipt.job_id,
                                                saved_receipt.lease,
                                                &saved_receipt.receipt) == 0
                     ? ED_OK : ED_ERR_REPORT;
        if (status != ED_OK) goto done;
        if (adapters->receipt_store.clear(adapters->receipt_store.context) != 0) {
            status = ED_ERR_STORAGE;
            goto done;
        }
    } else {
        if (adapters->http.open_fresh_session(adapters->http.context, session,
                                              sizeof(session)) != 0)
            return ED_ERR_SESSION;
        session_open = 1;
    }
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
        assignment.lease[0] == '\0' || assignment.total == 0 ||
        assignment.total > assignment.content.size) {
        status = ED_ERR_PROTOCOL;
        goto done;
    }

    range.http = &adapters->http;
    range.session = session;
    range.assignment = &assignment;
    status = ed_stage_verify(&assignment.content, fetch_assignment_range,
                             &range, &adapters->storage, &verified);
    if (status != ED_OK) {
        status = report_failed_assignment(&adapters->http, &adapters->receipt_store, session,
                                          &assignment, status);
        goto done;
    }

    memset(&active, 0, sizeof(active));
    active.http = &adapters->http;
    active.session = session;
    active.assignment = &assignment;
    /* Controller owns the line buffer and UART acknowledgments. The verified
     * opaque job is handed to that adapter exactly once; no second send path. */
    status = adapters->controller.stream_verified(adapters->controller.context,
                                                   verified,
                                                   poll_active, &active, &receipt);
    ed_verified_release(verified);
    verified = NULL;
    if (status < 0 || active.failed) {
        status = report_failed_progress(&adapters->http, &adapters->receipt_store, session, &assignment,
                                        active.failed ? ED_ERR_SESSION : ED_ERR_CONTROLLER,
                                        &active);
        goto done;
    }
    if (status == 1) receipt.state = ED_TERMINAL_CANCELED;
    else if (status != 0) {
        status = report_failed_progress(&adapters->http, &adapters->receipt_store, session, &assignment,
                                        ED_ERR_CONTROLLER, &active);
        goto done;
    }
    status = receipt_outcome(receipt.state, &outcome);
    if (status != ED_OK ||
        !terminated(receipt.controller_state, sizeof(receipt.controller_state)) ||
        !terminated(receipt.error, sizeof(receipt.error)) ||
        receipt.acked > receipt.total ||
        receipt.total == 0 ||
        !active.have_progress || receipt.acked != active.acked ||
        receipt.total != active.total ||
        (receipt.state == ED_TERMINAL_COMPLETED &&
         (receipt.acked != receipt.total || !receipt.controller_idle ||
          strcmp(receipt.controller_state, "Idle") != 0 ||
          receipt.error[0] != '\0'))) {
        status = report_failed_progress(&adapters->http, &adapters->receipt_store, session, &assignment,
                                        ED_ERR_PROTOCOL, &active);
        goto done;
    }
    if (receipt.state == ED_TERMINAL_CANCELED &&
        (receipt.acked > receipt.total || receipt.total == 0 ||
         strcmp(receipt.controller_state, "Idle") != 0)) {
        status = report_failed_progress(&adapters->http, &adapters->receipt_store, session, &assignment,
                                        ED_ERR_PROTOCOL, &active);
        goto done;
    }
    status = save_and_report(&adapters->http, &adapters->receipt_store, session,
                             assignment.job_id, assignment.lease, &receipt);
    if (status != ED_OK) {
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

#ifndef EMBERDECK_WORKER_CYCLE_H
#define EMBERDECK_WORKER_CYCLE_H

#include "job_core.h"

#define ED_WORKER_SESSION_BYTES 128u
#define ED_WORKER_JOB_ID_BYTES 128u
#define ED_WORKER_LEASE_BYTES 128u
#define ED_WORKER_CONTROLLER_STATE_BYTES 32u
#define ED_WORKER_ERROR_BYTES 128u

typedef struct {
    char job_id[ED_WORKER_JOB_ID_BYTES];
    char lease[ED_WORKER_LEASE_BYTES];
    ed_manifest content;
} ed_worker_assignment;

typedef enum {
    ED_TERMINAL_COMPLETED = 1,
    ED_TERMINAL_FAILED,
    ED_TERMINAL_CANCELED
} ed_terminal_state;

typedef struct {
    ed_terminal_state state;
    size_t acked;
    size_t total;
    int controller_idle;
    char controller_state[ED_WORKER_CONTROLLER_STATE_BYTES];
    char error[ED_WORKER_ERROR_BYTES];
} ed_terminal_receipt;

/* These callbacks form the portable boundary for a future ESP-IDF adapter.
 * open_fresh_session must create a unique session for this cycle. poll is
 * called exactly once. Every fetch and terminal receipt is bound to that
 * session, assignment, and lease. */
typedef struct {
    void *context;
    int (*open_fresh_session)(void *context, char *session, size_t capacity);
    int (*poll_assignment)(void *context, const char *session,
                           ed_worker_assignment *assignment,
                           int *has_assignment);
    int (*fetch_range)(void *context, const char *session, const char *job_id,
                       const char *lease, size_t offset, size_t max_length,
                       unsigned char *destination, size_t capacity,
                       size_t *received);
    int (*report_terminal)(void *context, const char *session,
                           const char *job_id, const char *lease,
                           const ed_terminal_receipt *receipt);
    void (*close_session)(void *context, const char *session);
} ed_http_worker_adapter;

typedef struct {
    void *context;
    /* Fill NUL-terminated receipt strings and return only after the controller
     * has a terminal state. A completed receipt must represent all source-line
     * acknowledgments and controller Idle, not merely successful UART writes. */
    int (*wait_terminal)(void *context, ed_terminal_receipt *receipt);
} ed_controller_adapter;

typedef struct {
    ed_http_worker_adapter http;
    ed_controller_adapter controller;
    ed_storage storage;
    ed_uart_sink uart_sink;
    void *uart_context;
} ed_worker_adapters;

typedef enum {
    ED_WORKER_ERROR = 0,
    ED_WORKER_NO_ASSIGNMENT,
    ED_WORKER_COMPLETED,
    ED_WORKER_FAILED,
    ED_WORKER_CANCELED
} ed_worker_outcome;

typedef struct {
    ed_worker_outcome outcome;
    size_t acked;
    size_t total;
} ed_worker_result;

/* Runs one fresh session and at most one assigned lease. The content is
 * staged and verified before the UART sink is called. No retry persistence
 * is provided; all exits close the session. */
ed_result ed_worker_run_one(const ed_worker_adapters *adapters,
                            ed_worker_result *result);

#endif

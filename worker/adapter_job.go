package worker

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/driveradapter"
)

func runAdapterJob(ctx context.Context, client *http.Client, endpoint, key, sessionID string, a assignment, registration adapterRegistration, report *controllerReport) (progress, error) {
	if len(a.Job.GCode) > driveradapter.MaxProgramBytes {
		return adapterFailure(a, "job program exceeds the 4 MiB worker limit", 0), nil
	}
	total, err := driver.Validate(a.Job.GCode)
	if err != nil {
		return adapterFailure(a, err.Error(), 0), nil
	}
	controller, err := driveradapter.Configure(ctx, registration.adapter, registration.deviceID, registration.config)
	if err != nil {
		return adapterFailure(a, err.Error(), total), nil
	}
	defer controller.Close()
	input := driveradapter.NewAssignment(a.Job.ID, a.Lease, []byte(a.Job.GCode))
	if err := controller.Start(ctx, input); err != nil {
		return adapterFailure(a, fmt.Sprintf("adapter Start: %v", err), total), nil
	}
	if a.Command == "pause" {
		if err := controller.Pause(ctx); err != nil {
			return adapterFailure(a, fmt.Sprintf("adapter Pause: %v", err), total), nil
		}
	}
	if a.Command == "resume" {
		if err := controller.Resume(ctx); err != nil {
			return adapterFailure(a, fmt.Sprintf("adapter Resume: %v", err), total), nil
		}
	}

	for {
		status, statusErr := controller.Status(ctx)
		if statusErr != nil {
			return adapterFailure(a, fmt.Sprintf("adapter Status: %v", statusErr), total), nil
		}
		p := progress{JobID: a.Job.ID, Lease: a.Lease, Acked: status.AcceptedLines, Total: status.TotalLines, ControllerState: status.ControllerState}
		switch status.State {
		case driveradapter.StateCompleted:
			p.State = "completed"
			return p, nil
		case driveradapter.StateFailed, driveradapter.StateOffline:
			p.State = "failed"
			p.Error = "adapter reported " + string(status.State)
			return p, nil
		case driveradapter.StateRunning, driveradapter.StateConnecting, driveradapter.StateCancelling:
			// The HTTP lease API has no connecting/cancelling progress state.
			// Keep the lease live with running until the adapter reports a
			// server-supported state or a terminal receipt.
			p.State = "running"
		case driveradapter.StatePaused:
			p.State = "paused"
		default:
			return adapterFailure(a, fmt.Sprintf("adapter Status returned invalid execution state %q", status.State), total), nil
		}
		var response assignment
		err := post(ctx, client, endpoint, key, report.poll(sessionID, &p), &response)
		if err != nil {
			return progress{}, fmt.Errorf("worker: progress report: %w", err)
		}
		if response.Lease != a.Lease || response.Job.ID != a.Job.ID {
			return progress{}, fmt.Errorf("worker: server changed active assignment")
		}
		switch response.Command {
		case "run":
		case "pause":
			if err := controller.Pause(ctx); err != nil {
				return adapterFailure(a, fmt.Sprintf("adapter Pause: %v", err), total), nil
			}
		case "resume":
			if err := controller.Resume(ctx); err != nil {
				return adapterFailure(a, fmt.Sprintf("adapter Resume: %v", err), total), nil
			}
		case "cancel":
			if err := controller.Cancel(ctx); err != nil {
				return adapterFailure(a, fmt.Sprintf("adapter Cancel: %v", err), total), nil
			}
			return progress{JobID: a.Job.ID, Lease: a.Lease, State: "canceled", Acked: status.AcceptedLines, Total: status.TotalLines, ControllerState: string(driveradapter.StateCancelling)}, nil
		default:
			return adapterFailure(a, fmt.Sprintf("worker: unsupported adapter command %q", response.Command), total), nil
		}
		if err := wait(ctx, reportInterval); err != nil {
			_ = controller.Cancel(context.Background())
			return progress{JobID: a.Job.ID, Lease: a.Lease, State: "canceled", Acked: status.AcceptedLines, Total: status.TotalLines}, nil
		}
	}
}

func adapterFailure(a assignment, message string, total int) progress {
	return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Error: message, Total: total}
}

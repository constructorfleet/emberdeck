package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

type manualAssignment struct {
	Action        string           `json:"action"`
	Axis          string           `json:"axis,omitempty"`
	DistanceMM    float64          `json:"distance_mm,omitempty"`
	FeedMMMin     float64          `json:"feed_mm_min,omitempty"`
	JobID         string           `json:"job_id,omitempty"`
	Bounds        *toolpath.Bounds `json:"bounds,omitempty"`
	LaserOff      bool             `json:"laser_off"`
	MaxDurationMS int64            `json:"max_duration_ms"`
}

func manualActionLimit(action string) (time.Duration, bool) {
	switch action {
	case "home":
		return 30 * time.Second, true
	case "unlock", "set_zero":
		return 5 * time.Second, true
	case "jog":
		return 10 * time.Second, true
	case "frame":
		return 60 * time.Second, true
	default:
		return 0, false
	}
}

func validateManualAssignment(a *manualAssignment) error {
	if a == nil {
		return errors.New("manual assignment is missing")
	}
	limit, ok := manualActionLimit(a.Action)
	if !ok {
		return fmt.Errorf("unsupported manual action %q", a.Action)
	}
	if !a.LaserOff {
		return errors.New("manual assignment must require laser off")
	}
	if a.MaxDurationMS <= 0 || time.Duration(a.MaxDurationMS)*time.Millisecond > limit {
		return errors.New("manual assignment duration is invalid")
	}
	if a.Action == "jog" {
		return driver.ValidateJogRequest(driver.JogRequest{Axis: a.Axis, DistanceMM: a.DistanceMM, FeedMMMin: a.FeedMMMin, Confirmed: true})
	}
	if a.Action == "frame" {
		if a.JobID == "" {
			return errors.New("frame assignment has no reviewed job ID")
		}
		if a.Bounds == nil {
			return errors.New("frame assignment has no reviewed bounds")
		}
		_, _, _, _, err := manualCommandBounds(*a.Bounds)
		return err
	}
	if a.Axis != "" || a.DistanceMM != 0 || a.FeedMMMin != 0 || a.JobID != "" || a.Bounds != nil {
		return errors.New("manual assignment contains unexpected motion parameters")
	}
	return nil
}

func manualCommandBounds(bounds toolpath.Bounds) (float64, float64, float64, float64, error) {
	values := []float64{bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -500 || v > 500 {
			return 0, 0, 0, 0, errors.New("frame bounds exceed worker motion limits")
		}
	}
	if bounds.Min.X >= bounds.Max.X || bounds.Min.Y >= bounds.Max.Y {
		return 0, 0, 0, 0, errors.New("frame bounds must have positive width and height")
	}
	return bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y, nil
}

func runManualLease(ctx context.Context, client *http.Client, endpoint, key, sessionID string, connect Connector, a assignment, report *controllerReport) error {
	if err := validateManualAssignment(a.Manual); err != nil {
		return postManualTerminal(ctx, client, endpoint, key, report, sessionID, a.Lease, "failed", err.Error(), "")
	}
	limit, _ := manualActionLimit(a.Manual.Action)
	if requested := time.Duration(a.Manual.MaxDurationMS) * time.Millisecond; requested < limit {
		limit = requested
	}
	actionCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := connect(actionCtx)
		if err != nil {
			done <- fmt.Errorf("worker: open controller for manual action: %w", err)
			return
		}
		if conn == nil {
			done <- errors.New("worker: connector returned nil connection")
			return
		}
		if a.Manual.Action == "jog" {
			err = driver.Jog(actionCtx, conn, driver.JogRequest{Axis: a.Manual.Axis, DistanceMM: a.Manual.DistanceMM, FeedMMMin: a.Manual.FeedMMMin, Confirmed: true})
		} else {
			bounds := toolpath.Bounds{}
			if a.Manual.Bounds != nil {
				bounds = *a.Manual.Bounds
			}
			err = driver.ManualCommand(actionCtx, conn, driver.ManualCommandRequest{Action: a.Manual.Action, Confirmed: true, Bounds: bounds})
		}
		done <- err
	}()
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	cancelRequested := false
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			state := "completed"
			controllerState := "Idle"
			message := ""
			if err != nil {
				if cancelRequested {
					state = "canceled"
				} else {
					state = "failed"
					message = truncate(err.Error(), 1000)
				}
				report.probeStatus(ctx, connect)
				controllerState = report.currentControllerState()
			}
			return postManualTerminal(ctx, client, endpoint, key, report, sessionID, a.Lease, state, message, controllerState)
		case <-ctx.Done():
			cancel()
			<-done
			return ctx.Err()
		case <-ticker.C:
			request := report.poll(sessionID, nil)
			request.ManualLease = a.Lease
			request.ManualState = "running"
			var response assignment
			if err := post(ctx, client, endpoint, key, request, &response); err != nil {
				cancel()
				<-done
				return fmt.Errorf("worker: manual-control progress report: %w", err)
			}
			switch response.Command {
			case "manual_running":
			case "manual_cancel":
				cancelRequested = true
				cancel()
			case "manual":
				// A duplicate pickup response is safe only before this worker has
				// reported progress; do not restart an action already in flight.
				cancel()
				<-done
				return errors.New("worker: server replayed a manual assignment while it was running")
			default:
				cancel()
				<-done
				return fmt.Errorf("worker: unexpected manual progress command %q", response.Command)
			}
		}
	}
}

func (r *controllerReport) currentControllerState() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.controllerState
}

func postManualTerminal(ctx context.Context, client *http.Client, endpoint, key string, report *controllerReport, sessionID, lease, state, message, controllerState string) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		request := report.poll(sessionID, nil)
		request.ManualLease = lease
		request.ManualState = state
		request.ManualError = message
		if controllerState != "" {
			request.ControllerState = controllerState
		}
		var response assignment
		err := post(ctx, client, endpoint, key, request, &response)
		if err == nil {
			return nil
		}
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code < 500 {
			return fmt.Errorf("worker: manual terminal report rejected: %w", err)
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
}

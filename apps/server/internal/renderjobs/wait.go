package renderjobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// Requester is what RenderAndWait needs from the service. *Service is the real one; the agent's tests use a fake.
type Requester interface {
	Request(ctx context.Context, who Caller, project ids.ProjectID, version int, profile string) (Render, bool, *Error)
	Get(ctx context.Context, ws ids.WorkspaceID, id ids.RenderID) (Render, *Error)
}

var _ Requester = (*Service)(nil)

// RenderAndWait looks at the render after pollFirst, then waits longer each time, up to pollLongest: a render is seen ready at most
// half a second after it is.
const (
	pollFirst   = 100 * time.Millisecond
	pollLongest = 500 * time.Millisecond
)

// RenderAndWait asks for a render of the version of the project (see Service.Request: an existing render of that version and
// profile is reused) and waits until it is ready, at most timeout. It is the Go entry point of the editor agent's
// render_preview tool, which then reads the contact sheet (Service.ContactSheetJPEG) and the duration of the render it returns.
//
// It returns the ready render and nil. For a render that failed it returns the render and a *FailedError (errors.Is with
// ErrRenderFailed); at the deadline, the render as it was last seen and an error that wraps ErrWaitTimeout (the render goes on,
// and asking again returns it); for a request that was refused, the *Error of the request. The context's own cancellation is
// returned as it is.
func RenderAndWait(ctx context.Context, svc Requester, who Caller, project ids.ProjectID, version int, profile string, timeout time.Duration) (Render, error) {
	if timeout <= 0 {
		return Render{}, errors.New("renderjobs: RenderAndWait needs a positive timeout")
	}
	deadline := time.Now().Add(timeout)
	r, _, e := svc.Request(ctx, who, project, version, profile)
	if e != nil {
		return Render{}, e
	}
	wait := pollFirst
	for {
		switch r.Status {
		case store.RenderReady:
			return r, nil
		case store.RenderFailed:
			reason := ""
			if r.FailureReason != nil {
				reason = *r.FailureReason
			}
			return r, &FailedError{Reason: reason}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return r, fmt.Errorf("%w: render %s is %s at %d%% after %v", ErrWaitTimeout, r.ID, r.Status, r.Progress, timeout)
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(min(wait, left)):
		}
		wait = min(wait*2, pollLongest)
		next, e := svc.Get(ctx, who.Workspace, r.ID)
		if e != nil {
			return r, e
		}
		r = next
	}
}

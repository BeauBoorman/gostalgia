package sdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// PresentationVersion versions the data/action protocol, independently of an
// app's manifest version. Apps supply data, never terminal widgets or keymaps.
const PresentationVersion = 1

type ViewState string

const (
	ViewReady   ViewState = "ready"
	ViewLoading ViewState = "loading"
	ViewError   ViewState = "error"
)

type Item struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// Field is a single-line text input. IDs, not labels or screen positions, route
// input back to the app. The experience owns editing and focus.
type Field struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
}

type Action struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// View is a complete snapshot, not a patch or a tree of speculative widgets.
// Present supplies Version and Instance; Instance changes on every launch.
type View struct {
	Version  int       `json:"version"`
	Instance string    `json:"instance"`
	Title    string    `json:"title"`
	State    ViewState `json:"state"`
	Items    []Item    `json:"items"`
	Fields   []Field   `json:"fields"`
	Actions  []Action  `json:"actions"`
	Status   string    `json:"status"`
	Error    string    `json:"error"`
}

type ViewRequest struct {
	Version int `json:"version"`
}

// ActionRequest carries semantic input only. RequestID identifies an in-flight
// operation for cancellation; it is not a durable idempotency key.
type ActionRequest struct {
	Version   int               `json:"version"`
	Instance  string            `json:"instance"`
	RequestID string            `json:"request_id"`
	Action    string            `json:"action"`
	ItemID    string            `json:"item_id,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
}

type CancelRequest struct {
	Version   int    `json:"version"`
	Instance  string `json:"instance"`
	RequestID string `json:"request_id"`
}

type CancelResult struct {
	Canceled bool `json:"canceled"`
}

// Validate bounds snapshots and rejects ambiguous IDs and unsupported states.
// All text remains untrusted; the experience must strip terminal controls.
func (v View) Validate() error {
	if v.Version != PresentationVersion || v.Instance == "" {
		return fmt.Errorf("presentation: unsupported version or missing instance")
	}
	if strings.TrimSpace(v.Title) == "" || len(v.Title) > 256 ||
		len(v.Status) > 4096 || len(v.Error) > 4096 {
		return fmt.Errorf("presentation: invalid title, status, or error")
	}
	switch v.State {
	case ViewReady, ViewLoading:
	case ViewError:
		if strings.TrimSpace(v.Error) == "" {
			return fmt.Errorf("presentation: error state requires an error banner")
		}
	default:
		return fmt.Errorf("presentation: unknown view state %q", v.State)
	}
	if len(v.Items) > 100 || len(v.Fields) > 16 || len(v.Actions) > 16 {
		return fmt.Errorf("presentation: snapshot exceeds item/field/action limits")
	}
	check := func(id, label string, seen map[string]bool) error {
		if !entryPattern.MatchString(id) || len(id) > 64 || seen[id] ||
			strings.TrimSpace(label) == "" || len(label) > 256 {
			return fmt.Errorf("presentation: invalid or duplicate ID/label %q", id)
		}
		seen[id] = true
		return nil
	}
	seen := make(map[string]bool)
	for _, item := range v.Items {
		if err := check(item.ID, item.Label, seen); err != nil {
			return err
		}
		if len(item.Detail) > 4096 {
			return fmt.Errorf("presentation: item detail exceeds limit")
		}
	}
	seen = make(map[string]bool)
	for _, field := range v.Fields {
		if err := check(field.ID, field.Label, seen); err != nil {
			return err
		}
		if len(field.Value) > 4096 {
			return fmt.Errorf("presentation: field value exceeds limit")
		}
	}
	seen = make(map[string]bool)
	for _, action := range v.Actions {
		if err := check(action.ID, action.Label, seen); err != nil {
			return err
		}
	}
	return nil
}

// Present declares view, action, and cancel during Init. Callbacks obey the
// usual app-scoped context and concurrency rules. Returning an error produces
// an IPC error; returning ViewError keeps a recoverable error banner on screen.
// Actions must honor cancellation before committing work. Cancellation is
// cooperative and cannot undo an operation that has already committed.
func (c *Context) Present(snapshot func(context.Context) (View, error), action func(context.Context, ActionRequest) (View, error)) error {
	if snapshot == nil || action == nil {
		return fmt.Errorf("presentation: snapshot and action callbacks are required")
	}
	instance := rand.Text()
	var mu sync.Mutex
	active := make(map[string]context.CancelFunc)
	// The socket can dispatch cancel before action. Remember a bounded recent
	// window of cancellation intents so that race does not execute canceled work.
	var canceled []string
	finish := func(v View, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		v.Version, v.Instance = PresentationVersion, instance
		return v, v.Validate()
	}
	check := func(version int, owner, request string) error {
		if version != PresentationVersion || owner != instance {
			return fmt.Errorf("presentation: unsupported version or stale instance")
		}
		if !entryPattern.MatchString(request) || len(request) > 64 {
			return fmt.Errorf("presentation: invalid request_id")
		}
		return nil
	}
	if err := c.Handle("view", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ViewRequest
		if err := decodePresentation(raw, &p); err != nil {
			return nil, err
		}
		if p.Version != PresentationVersion {
			return nil, fmt.Errorf("presentation: unsupported version %d", p.Version)
		}
		return finish(snapshot(ctx))
	}); err != nil {
		return err
	}
	if err := c.Handle("action", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p ActionRequest
		if err := decodePresentation(raw, &p); err != nil {
			return nil, err
		}
		if err := check(p.Version, p.Instance, p.RequestID); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		mu.Lock()
		for _, id := range canceled {
			if id == p.RequestID {
				mu.Unlock()
				return nil, context.Canceled
			}
		}
		if _, exists := active[p.RequestID]; exists || len(active) >= 16 {
			mu.Unlock()
			return nil, fmt.Errorf("presentation: duplicate or too many active requests")
		}
		active[p.RequestID] = cancel
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(active, p.RequestID)
			mu.Unlock()
		}()
		v, err := snapshot(ctx)
		if err != nil {
			return nil, err
		}
		v.Version, v.Instance = PresentationVersion, instance
		if err := v.Validate(); err != nil {
			return nil, err
		}
		if err := validateAction(v, p); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return finish(action(ctx, p))
	}); err != nil {
		return err
	}
	return c.Handle("cancel", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p CancelRequest
		if err := decodePresentation(raw, &p); err != nil {
			return nil, err
		}
		if err := check(p.Version, p.Instance, p.RequestID); err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if cancel := active[p.RequestID]; cancel != nil {
			cancel()
		}
		canceled = append(canceled, p.RequestID)
		if len(canceled) > 64 {
			canceled = canceled[len(canceled)-64:]
		}
		return CancelResult{Canceled: true}, nil
	})
}

func validateAction(v View, p ActionRequest) error {
	enabled := false
	for _, a := range v.Actions {
		if a.ID == p.Action && !a.Disabled {
			enabled = true
		}
	}
	if !enabled || v.State == ViewLoading {
		return fmt.Errorf("presentation: unknown, disabled, or loading action %q", p.Action)
	}
	if p.ItemID != "" {
		found := false
		for _, item := range v.Items {
			found = found || item.ID == p.ItemID
		}
		if !found {
			return fmt.Errorf("presentation: unknown item %q", p.ItemID)
		}
	}
	fields := make(map[string]bool)
	for _, f := range v.Fields {
		fields[f.ID] = true
		if f.Required && strings.TrimSpace(p.Values[f.ID]) == "" {
			return fmt.Errorf("presentation: field %q is required", f.ID)
		}
	}
	for id, value := range p.Values {
		if !fields[id] || len(value) > 4096 {
			return fmt.Errorf("presentation: unknown or oversized field %q", id)
		}
	}
	return nil
}

func decodePresentation(raw json.RawMessage, out any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("presentation: params object is required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("presentation: bad params: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("presentation: expected one params object")
	}
	return nil
}

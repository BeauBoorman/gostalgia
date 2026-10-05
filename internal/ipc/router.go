package ipc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Router maps method names to handlers. The same router serves in-process
// callers and socket clients, so the wire never defines a second API.
type Router struct {
	mu     sync.RWMutex
	routes map[string]Handler
}

func NewRouter() *Router {
	return &Router{routes: map[string]Handler{}}
}

// Handle registers a handler. Duplicate methods are rejected: routes are
// owned by exactly one subsystem or application instance.
func (r *Router) Handle(method string, h Handler) error {
	if method == "" || h == nil {
		return fmt.Errorf("ipc: invalid route %q", method)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.routes[method]; dup {
		return fmt.Errorf("ipc: route %q is already registered", method)
	}
	r.routes[method] = h
	return nil
}

// Unhandle removes one method.
func (r *Router) Unhandle(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, method)
}

// UnhandlePrefix removes every method with the prefix; applications use
// it to retract all their routes ("app/<id>") on exit.
func (r *Router) UnhandlePrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for method := range r.routes {
		if strings.HasPrefix(method, prefix) {
			delete(r.routes, method)
		}
	}
}

// Methods lists registered method names, sorted.
func (r *Router) Methods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.routes))
	for method := range r.routes {
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

// Dispatch routes one request. It never panics out and never returns a
// response with a mismatched ID.
func (r *Router) Dispatch(ctx context.Context, req Request) (resp Response) {
	resp.ID = req.ID
	defer func() {
		if p := recover(); p != nil {
			resp = Response{ID: req.ID, Error: fmt.Sprintf("internal error in %q: %v", req.Method, p)}
		}
	}()
	r.mu.RLock()
	h, ok := r.routes[req.Method]
	r.mu.RUnlock()
	if !ok {
		return Response{ID: req.ID, Error: fmt.Sprintf("unknown method %q", req.Method)}
	}
	data, err := h(ctx, req)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	enc, err := Encode(data)
	if err != nil {
		return Response{ID: req.ID, Error: err.Error()}
	}
	return Response{ID: req.ID, OK: true, Data: enc}
}

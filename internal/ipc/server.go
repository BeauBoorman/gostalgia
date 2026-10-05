package ipc

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"gostalgia/internal/security"
)

// maxLine bounds one protocol line (request or response).
const maxLine = 4 << 20 // 4 MiB

// Server serves the router over a local listener. Every connection must
// authenticate with the environment token before any other method is
// accepted; authenticated connections receive the admin capability set
// (trusted local peers — see docs/security.md).
type Server struct {
	ln     net.Listener
	router *Router
	token  string
	log    *slog.Logger

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
	done  chan struct{}
}

func NewServer(ln net.Listener, router *Router, token string, log *slog.Logger) *Server {
	return &Server{
		ln:     ln,
		router: router,
		token:  token,
		log:    log,
		conns:  map[net.Conn]struct{}{},
		done:   make(chan struct{}),
	}
}

// Serve accepts connections until Close is called. It returns nil after
// an intentional close.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
				return fmt.Errorf("ipc: accept: %w", err)
			}
		}
		// Track and arm the connection while holding the same mutex
		// Close sweeps s.conns under, and re-check done inside it. A
		// connection accepted concurrently with Close can then never
		// escape being closed, and wg.Add can never race wg.Wait from
		// a zero counter.
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			conn.Close()
			return nil
		default:
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handleConn(conn)
	}
}

// Close stops the listener and drops all live connections, including
// idle ones — shutdown must never hang on a connected client, and it
// never waits on a connection it did not close itself.
func (s *Server) Close() error {
	select {
	case <-s.done:
		return nil
	default:
		close(s.done)
	}
	err := s.ln.Close()
	s.mu.Lock()
	for conn := range s.conns {
		conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	conn.Close()
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer s.untrack(conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := bufio.NewScanner(conn)
	r.Buffer(make([]byte, 64*1024), maxLine)
	w := bufio.NewWriter(conn)

	// Handshake: the first request must be auth.
	req, err := readRequest(r)
	if err != nil {
		return
	}
	if req.Method != "auth" {
		_ = writeResponse(w, Response{ID: req.ID, Error: "unauthorized: first request must be auth"})
		return
	}
	var p struct {
		Token string `json:"token"`
	}
	if err := DecodeParams(req.Params, &p); err != nil {
		_ = writeResponse(w, Response{ID: req.ID, Error: err.Error()})
		return
	}
	if subtle.ConstantTimeCompare([]byte(p.Token), []byte(s.token)) != 1 {
		s.log.Warn("ipc: rejected connection with bad token", "remote", conn.RemoteAddr())
		_ = writeResponse(w, Response{ID: req.ID, Error: "unauthorized: bad token"})
		return
	}
	if err := writeResponse(w, Response{ID: req.ID, OK: true}); err != nil {
		return
	}

	authCtx := WithCapabilities(ctx, security.AdminCapabilities())
	for {
		req, err := readRequest(r)
		if err != nil {
			return
		}
		resp := s.router.Dispatch(authCtx, req)
		if err := writeResponse(w, resp); err != nil {
			return
		}
	}
}

func readRequest(r *bufio.Scanner) (Request, error) {
	if !r.Scan() {
		err := r.Err()
		if err == nil {
			err = errors.New("connection closed")
		}
		return Request{}, err
	}
	var req Request
	if err := json.Unmarshal(r.Bytes(), &req); err != nil {
		return Request{}, fmt.Errorf("ipc: bad request: %w", err)
	}
	return req, nil
}

func writeResponse(w *bufio.Writer, resp Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(Response{ID: resp.ID, Error: "response encoding failed"})
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

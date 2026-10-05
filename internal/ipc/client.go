package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// defaultCallTimeout applies when the caller passes no deadline.
const defaultCallTimeout = 30 * time.Second

// Client is an authenticated IPC client over one connection. Calls are
// serialized (one outstanding request per client); multiplexing is future
// work. Safe for concurrent use.
type Client struct {
	conn net.Conn
	r    *bufio.Scanner
	w    *bufio.Writer

	mu     sync.Mutex
	nextID int64
}

// NewClient authenticates a fresh connection with token and returns a
// ready client. Callers dial via platform.DialIPC.
func NewClient(conn net.Conn, token string) (*Client, error) {
	c := &Client{
		conn: conn,
		r:    bufio.NewScanner(conn),
		w:    bufio.NewWriter(conn),
	}
	c.r.Buffer(make([]byte, 64*1024), maxLine)
	err := c.call(context.Background(), "auth", map[string]string{"token": token}, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ipc: auth failed: %w", err)
	}
	return c, nil
}

// Call invokes method with params (nil for none) and, if out is
// non-nil, decodes the response data into it.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.call(ctx, method, params, out)
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID

	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = Encode(params)
		if err != nil {
			return err
		}
	}
	req := Request{ID: id, Method: method, Params: raw}

	timeout := defaultCallTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return errors.New("ipc: deadline exceeded before call")
	}
	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.w.Write(b); err != nil {
		return fmt.Errorf("ipc: write: %w", err)
	}
	if err := c.w.WriteByte('\n'); err != nil {
		return fmt.Errorf("ipc: write: %w", err)
	}
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("ipc: flush: %w", err)
	}

	for c.r.Scan() {
		var resp Response
		if err := json.Unmarshal(c.r.Bytes(), &resp); err != nil {
			return fmt.Errorf("ipc: bad response: %w", err)
		}
		if resp.ID != id {
			continue // unsolicited line; ignore for now
		}
		if !resp.OK {
			return errors.New(resp.Error)
		}
		if out != nil && len(resp.Data) > 0 {
			return json.Unmarshal(resp.Data, out)
		}
		return nil
	}
	err = c.r.Err()
	if err == nil {
		err = errors.New("connection closed")
	}
	return fmt.Errorf("ipc: read: %w", err)
}

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

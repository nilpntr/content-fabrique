package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// The Chrome DevTools Protocol over a pipe (--remote-debugging-pipe): Chrome reads commands from its file descriptor 3 and
// writes responses and events to its file descriptor 4, each message a JSON object followed by a NUL byte. No TCP port is
// opened, so nothing else in the sandbox (or outside it) can talk to the browser.

// maxMessageBytes caps one message from Chrome. A screenshot of a 1080x1920 frame is a few MB of base64; anything larger is
// refused rather than buffered without bound.
const maxMessageBytes = 64 << 20

// cdpError is an error answer to a command.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

// message is anything Chrome writes: a response (ID set) or an event (Method set).
type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

// event is one protocol event.
type event struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

// client sends commands and routes their answers. Events go to the events channel, which the caller must drain.
type client struct {
	w       io.Writer
	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	events  chan event
	done    chan struct{}
	err     error // why the read loop stopped
}

func newClient(r io.Reader, w io.Writer) *client {
	c := &client{w: w, pending: map[int64]chan message{}, events: make(chan event, 1024), done: make(chan struct{})}
	go c.readLoop(r)
	return c
}

// readLoop reads NUL-terminated messages until the pipe closes.
func (c *client) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	var err error
	for {
		var raw []byte
		raw, err = readMessage(br)
		if err != nil {
			break
		}
		var m message
		if err = json.Unmarshal(raw, &m); err != nil {
			err = fmt.Errorf("cdp: undecodable message: %w", err)
			break
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch, ok := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ok {
				ch <- m
			}
			continue
		}
		if m.Method != "" {
			c.events <- event{Method: m.Method, SessionID: m.SessionID, Params: m.Params}
		}
	}
	c.mu.Lock()
	if errors.Is(err, io.EOF) {
		err = errors.New("cdp: chrome closed the pipe")
	}
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
	close(c.events)
}

// readMessage reads one NUL-terminated message, refusing one over maxMessageBytes.
func readMessage(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice(0)
		if len(buf)+len(chunk) > maxMessageBytes {
			return nil, fmt.Errorf("cdp: a message from chrome is over %d bytes", maxMessageBytes)
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return buf[:len(buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// call sends a command and decodes its result into out (which may be nil). sessionID is "" for the browser target.
func (c *client) call(ctx context.Context, sessionID, method string, params, out any) error {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if sessionID != "" {
		req["sessionId"] = sessionID
	}
	data, err := json.Marshal(req)
	if err != nil {
		c.forget(id)
		return fmt.Errorf("cdp: encode %s: %w", method, err)
	}
	c.wmu.Lock()
	_, err = c.w.Write(append(data, 0))
	c.wmu.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("cdp: send %s: %w", method, err)
	}

	select {
	case m, ok := <-ch:
		if !ok {
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			return fmt.Errorf("cdp: %s: %w", method, err)
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if out != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, out); err != nil {
				return fmt.Errorf("cdp: decode the result of %s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return fmt.Errorf("cdp: %s: %w", method, ctx.Err())
	}
}

func (c *client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

package natsapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Transport is an http.RoundTripper that sends requests over NATS. Request
// URLs have the form nats://{service}/api/v1/..., so any code written
// against *http.Client works unchanged:
//
//	client := natsapi.NewClient(nc)
//	resp, err := client.Get(natsapi.URL("admin_service", "/api/v1/users"))
//
// Authentication is the same as over HTTPS: set the Authorization bearer
// and, optionally, X-Blackbox-AccountID on the request.
type Transport struct {
	// Conn is the NATS connection. Required.
	Conn *nats.Conn
	// Timeout bounds the wait for each response message. Default 30s.
	Timeout time.Duration
	// ChunkSize bounds each request body message; the connection's max
	// payload lowers it when needed. Default 256 KiB.
	ChunkSize int
}

// NewClient returns an *http.Client whose transport speaks NATS.
func NewClient(nc *nats.Conn) *http.Client {
	return &http.Client{Transport: &Transport{Conn: nc}}
}

// NewRequest builds a request for the Transport: method, service, and the
// path and query under /api/.
func NewRequest(ctx context.Context, method, service, path string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, URL(service, path), body)
}

func (t *Transport) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return DefaultTimeout
}

func (t *Transport) chunkSize() int {
	chunk := t.ChunkSize
	if chunk <= 0 {
		chunk = DefaultChunkSize
	}
	if max := int(t.Conn.MaxPayload()) - chunkHeadroom; max > 0 && max < chunk {
		chunk = max
	}
	if chunk <= 0 {
		chunk = 1024
	}
	return chunk
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Conn == nil {
		return nil, ErrNotConnected
	}
	service, path, err := splitURL(req.URL)
	if err != nil {
		return nil, err
	}
	subject, err := Subject(service, req.Method, path)
	if err != nil {
		return nil, err
	}
	ctx := req.Context()

	msg := &nats.Msg{Subject: subject, Header: nats.Header{}}
	for key, values := range req.Header {
		if isProtocolHeader(key) {
			continue
		}
		for _, v := range values {
			msg.Header.Add(key, v)
		}
	}
	msg.Header.Set(HeaderPath, path)

	// Read the first chunk of the body. If the body ends inside it, the
	// request is a single message; otherwise the service pulls the rest.
	var bodySub *nats.Subscription
	if req.Body != nil {
		chunk := t.chunkSize()
		first, rest, err := readChunk(req.Body, chunk)
		if err != nil {
			return nil, err
		}
		msg.Data = first
		if rest {
			server := &bodyServer{body: req.Body, chunk: chunk}
			bodySubject := nats.NewInbox()
			bodySub, err = t.Conn.Subscribe(bodySubject, server.serve)
			if err != nil {
				return nil, fmt.Errorf("natsapi: body subscription: %w", err)
			}
			msg.Header.Set(HeaderBodyStream, bodySubject)
		}
	}

	inbox := nats.NewInbox()
	sub, err := t.Conn.SubscribeSync(inbox)
	if err != nil {
		unsubscribe(bodySub)
		return nil, fmt.Errorf("natsapi: reply subscription: %w", err)
	}
	msg.Reply = inbox
	if err := t.Conn.PublishMsg(msg); err != nil {
		unsubscribe(sub, bodySub)
		return nil, fmt.Errorf("natsapi: publish: %w", err)
	}

	body := &chunkBody{sub: sub, bodySub: bodySub, timeout: t.timeout(), ctx: ctx}
	first, err := body.next()
	if err != nil {
		body.Close()
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, ErrNoService
		}
		return nil, err
	}
	// Reply headers are read through an http.Header for case-insensitivity.
	hdr := http.Header{}
	for key, values := range first.Header {
		for _, v := range values {
			hdr.Add(key, v)
		}
	}
	if hdr.Get("Status") == "503" && len(first.Data) == 0 {
		body.Close()
		return nil, ErrNoService
	}
	status, err := strconv.Atoi(hdr.Get(HeaderStatus))
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("natsapi: reply without %s header", HeaderStatus)
	}
	resp := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{},
		Body:          body,
		Request:       req,
		ContentLength: -1,
	}
	for key, values := range hdr {
		if isProtocolHeader(key) || key == "Status" || key == "Description" {
			continue
		}
		resp.Header[key] = values
	}
	body.buf = first.Data
	body.done = hdr.Get(HeaderEnd) != ""
	if body.done {
		resp.ContentLength = int64(len(first.Data))
	}
	return resp, nil
}

// readChunk reads up to chunk bytes and reports whether more may follow.
func readChunk(r io.Reader, chunk int) (data []byte, more bool, err error) {
	buf := make([]byte, chunk)
	n, err := io.ReadFull(r, buf)
	switch {
	case err == nil:
		// The chunk filled; the body may continue. When it ends exactly
		// here the service's next pull answers with an empty final chunk.
		return buf[:n], true, nil
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return buf[:n], false, nil
	default:
		return nil, false, fmt.Errorf("natsapi: read body: %w", err)
	}
}

// bodyServer answers the service's chunk requests from the request body.
type bodyServer struct {
	mu    sync.Mutex
	body  io.Reader
	chunk int
	done  bool
}

func (b *bodyServer) serve(m *nats.Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	reply := &nats.Msg{Subject: m.Reply, Header: nats.Header{}}
	if b.done {
		reply.Header.Set(HeaderEnd, "1")
		_ = m.RespondMsg(reply)
		return
	}
	data, more, err := readChunk(b.body, b.chunk)
	if err != nil || !more {
		b.done = true
		reply.Header.Set(HeaderEnd, "1")
	}
	reply.Data = data
	_ = m.RespondMsg(reply)
}

// chunkBody is the response body: a stream of reply messages until one
// carries HeaderEnd.
type chunkBody struct {
	sub     *nats.Subscription
	bodySub *nats.Subscription
	timeout time.Duration
	ctx     context.Context
	buf     []byte
	done    bool
	closed  bool
}

func (c *chunkBody) next() (*nats.Msg, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	msg, err := c.sub.NextMsgWithContext(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && c.ctx.Err() == nil {
			return nil, fmt.Errorf("natsapi: timed out waiting for the service: %w", err)
		}
		return nil, err
	}
	return msg, nil
}

func (c *chunkBody) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.done {
			return 0, io.EOF
		}
		msg, err := c.next()
		if err != nil {
			c.done = true
			return 0, err
		}
		c.buf = msg.Data
		c.done = msg.Header.Get(HeaderEnd) != ""
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *chunkBody) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	unsubscribe(c.sub, c.bodySub)
	return nil
}

func unsubscribe(subs ...*nats.Subscription) {
	for _, s := range subs {
		if s != nil {
			_ = s.Unsubscribe()
		}
	}
}

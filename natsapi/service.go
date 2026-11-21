package natsapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
)

// Options configures a Service.
type Options struct {
	// Service is the subject token and micro service name, for example
	// "admin_service". Required.
	Service string
	// Handler receives every bridged request, normally the service's
	// router with all of its middleware. Required.
	Handler http.Handler
	// Version is the micro service version (semver). Default "1.0.0".
	Version string
	// Description is published in the micro service info.
	Description string
	// QueueGroup is shared by every instance of the service so that each
	// request reaches exactly one of them. Default "api".
	QueueGroup string
	// ChunkSize bounds each body message; the connection's max payload
	// lowers it when needed. Default 256 KiB.
	ChunkSize int
	// Timeout bounds the wait for each request body chunk pulled from the
	// caller. Default 30s.
	Timeout time.Duration
	// Logger receives one line per request. Default: a new platform logger.
	Logger *logger.Logger
}

// Service bridges NATS requests to an http.Handler. One Service can serve
// any number of NATS connections, one per account, so a multi-tenant
// deployment serves every tenant with the same handler.
type Service struct {
	o  Options
	l  *logger.Logger
	mu sync.Mutex
	// accounts are the served connections by account ID. The empty string
	// is a valid key for a connection whose account is not known.
	accounts map[string]*served
	closed   bool
}

type served struct {
	accountID string
	nc        *nats.Conn
	svc       micro.Service
	chunk     int
}

// New validates the options and returns a Service that serves nothing until
// Serve is called.
func New(o Options) (*Service, error) {
	if !ValidService(o.Service) {
		return nil, fmt.Errorf("natsapi: invalid service name %q", o.Service)
	}
	if o.Handler == nil {
		return nil, errors.New("natsapi: Handler is required")
	}
	if o.Version == "" {
		o.Version = DefaultVersion
	}
	if o.QueueGroup == "" {
		o.QueueGroup = DefaultQueueGroup
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = DefaultChunkSize
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	l := o.Logger
	if l == nil {
		l = logger.NewLogger("platform", "natsapi")
	}
	return &Service{o: o, l: l, accounts: make(map[string]*served)}, nil
}

// Name returns the service token.
func (s *Service) Name() string { return s.o.Service }

// Serve starts answering requests on nc. accountID is the NATS account the
// connection belongs to; every request on it is pinned to that account (a
// mismatching X-Blackbox-AccountID is refused with 403) and the ID
// middleware sees it as the account header. Pass "" to skip the pin.
// Serving the same account twice is a no-op.
func (s *Service) Serve(accountID string, nc *nats.Conn) error {
	if nc == nil {
		return ErrNotConnected
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("natsapi: service is closed")
	}
	if _, ok := s.accounts[accountID]; ok {
		return nil
	}
	sv := &served{accountID: accountID, nc: nc, chunk: s.chunkSize(nc)}
	svc, err := micro.AddService(nc, micro.Config{
		Name:        s.o.Service,
		Version:     s.o.Version,
		Description: s.o.Description,
		QueueGroup:  s.o.QueueGroup,
		Metadata: map[string]string{
			"transport": "http",
			"account":   accountID,
		},
	})
	if err != nil {
		return fmt.Errorf("natsapi: add service: %w", err)
	}
	for _, method := range Methods {
		m := method
		err := svc.AddEndpoint(m, micro.HandlerFunc(func(req micro.Request) {
			go s.handle(sv, req)
		}), micro.WithEndpointSubject(EndpointSubject(s.o.Service, m)))
		if err != nil {
			_ = svc.Stop()
			return fmt.Errorf("natsapi: add endpoint %s: %w", m, err)
		}
	}
	// Flush so the subscriptions are registered server-side before Serve
	// returns; otherwise a request sent right away can find no responder.
	if err := nc.Flush(); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("natsapi: flush: %w", err)
	}
	sv.svc = svc
	s.accounts[accountID] = sv
	s.l.InfoE().Str("service", s.o.Service).Str("accountID", accountID).Msg("natsapi: serving")
	return nil
}

// Stop stops serving one account's connection. The connection stays open.
func (s *Service) Stop(accountID string) error {
	s.mu.Lock()
	sv, ok := s.accounts[accountID]
	delete(s.accounts, accountID)
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return sv.svc.Stop()
}

// Close stops serving every connection.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	all := s.accounts
	s.accounts = make(map[string]*served)
	s.mu.Unlock()
	for _, sv := range all {
		_ = sv.svc.Stop()
	}
}

// Accounts returns the IDs of the accounts currently served.
func (s *Service) Accounts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) chunkSize(nc *nats.Conn) int {
	chunk := s.o.ChunkSize
	if max := int(nc.MaxPayload()) - chunkHeadroom; max > 0 && max < chunk {
		chunk = max
	}
	if chunk <= 0 {
		chunk = 1024
	}
	return chunk
}

// handle bridges one request. It runs on its own goroutine.
func (s *Service) handle(sv *served, req micro.Request) {
	start := time.Now()
	w := newResponseWriter(sv.nc, req.Reply(), sv.chunk)
	defer func() {
		if r := recover(); r != nil {
			s.l.ErrorE().Str("service", s.o.Service).Str("panic", fmt.Sprint(r)).
				Str("stack", string(debug.Stack())).Msg("natsapi: handler panic")
			if !w.sent {
				w.header = http.Header{}
				w.wroteHeader = false
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}
		w.finish()
	}()

	r, body, code, reason := s.buildRequest(sv, req)
	if code != 0 {
		http.Error(w, reason, code)
		s.log(sv, req.Subject(), http.Header(req.Headers()).Get(HeaderPath), code, start)
		return
	}
	if body != nil {
		defer body.Close()
	}
	s.o.Handler.ServeHTTP(w, r)
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	s.log(sv, req.Subject(), r.URL.RequestURI(), w.status, start)
}

func (s *Service) log(sv *served, subject, path string, status int, start time.Time) {
	s.l.InfoE().Str("service", s.o.Service).Str("accountID", sv.accountID).
		Str("subject", subject).Str("path", path).Int("status", status).
		Dur("duration", time.Since(start)).Msg("natsapi: request")
}

// buildRequest turns the NATS message into an *http.Request. A non-zero
// code means the request was refused before reaching the handler.
func (s *Service) buildRequest(sv *served, req micro.Request) (r *http.Request, body io.ReadCloser, code int, reason string) {
	// NATS headers are case-sensitive and HTTP headers are not; read them
	// through an http.Header so a caller's spelling never matters.
	hdr := http.Header{}
	for key, values := range req.Headers() {
		for _, v := range values {
			hdr.Add(key, v)
		}
	}
	path := hdr.Get(HeaderPath)
	if path == "" {
		return nil, nil, http.StatusBadRequest, "missing " + HeaderPath + " header"
	}
	if !strings.HasPrefix(path, "/api/") {
		return nil, nil, http.StatusNotFound, "not found"
	}
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return nil, nil, http.StatusBadRequest, "bad path"
	}
	_, method, resource, ok := parseSubject(req.Subject())
	if !ok {
		return nil, nil, http.StatusBadRequest, "bad subject"
	}
	if resource != ResourceToken(path) {
		return nil, nil, http.StatusBadRequest, "subject does not match path"
	}
	// The connection's account is authoritative. A caller may name it or
	// leave it out, but may not name another one.
	if sv.accountID != "" {
		if h := hdr.Get(HeaderAccountID); h != "" && h != sv.accountID {
			return nil, nil, http.StatusForbidden, "account does not match connection"
		}
	}

	var rc io.ReadCloser
	var length int64 = -1
	if stream := hdr.Get(HeaderBodyStream); stream != "" {
		rc = &pullReader{nc: sv.nc, subject: stream, timeout: s.o.Timeout, first: req.Data()}
	} else {
		data := req.Data()
		rc = io.NopCloser(bytes.NewReader(data))
		length = int64(len(data))
	}

	r, err = http.NewRequestWithContext(context.Background(), method, u.String(), rc)
	if err != nil {
		return nil, nil, http.StatusBadRequest, "bad request"
	}
	r.ContentLength = length
	r.RequestURI = path
	r.Host = s.o.Service
	r.RemoteAddr = TransportNATS + ":" + sv.accountID
	for key, values := range hdr {
		if isProtocolHeader(key) || key == "Content-Length" {
			continue
		}
		r.Header[key] = values
	}
	r.Header.Set(HeaderTransport, TransportNATS)
	if sv.accountID != "" {
		r.Header.Set(HeaderAccountID, sv.accountID)
	}
	return r, rc, 0, ""
}

// pullReader reads a request body the caller serves chunk by chunk. The
// first chunk arrived in the request message itself.
type pullReader struct {
	nc      *nats.Conn
	subject string
	timeout time.Duration
	first   []byte
	started bool
	seq     int
	buf     []byte
	done    bool
}

func (p *pullReader) Read(b []byte) (int, error) {
	if !p.started {
		p.started = true
		p.buf = p.first
		p.first = nil
	}
	for len(p.buf) == 0 {
		if p.done {
			return 0, io.EOF
		}
		msg, err := p.nc.RequestMsg(&nats.Msg{
			Subject: p.subject,
			Header:  nats.Header{HeaderSeq: []string{strconv.Itoa(p.seq)}},
		}, p.timeout)
		if err != nil {
			p.done = true
			return 0, fmt.Errorf("natsapi: body chunk %d: %w", p.seq, err)
		}
		p.seq++
		p.buf = msg.Data
		if msg.Header.Get(HeaderEnd) != "" {
			p.done = true
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *pullReader) Close() error { return nil }

// responseWriter publishes the handler's response to the reply inbox in
// chunks. The first message carries the status and headers; the last
// carries HeaderEnd.
type responseWriter struct {
	nc          *nats.Conn
	reply       string
	chunk       int
	header      http.Header
	status      int
	wroteHeader bool
	buf         bytes.Buffer
	seq         int
	sent        bool
	err         error
}

func newResponseWriter(nc *nats.Conn, reply string, chunk int) *responseWriter {
	return &responseWriter{nc: nc, reply: reply, chunk: chunk, header: http.Header{}}
}

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(p) > 0 {
		n := w.chunk - w.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		w.buf.Write(p[:n])
		p = p[n:]
		total += n
		if w.buf.Len() >= w.chunk {
			if err := w.publish(false); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// Flush sends what is buffered so far, so handlers that stream can push
// partial output without waiting for a full chunk.
func (w *responseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.buf.Len() > 0 {
		_ = w.publish(false)
	}
}

// finish sends the remaining bytes with the end marker.
func (w *responseWriter) finish() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return
	}
	_ = w.publish(true)
}

func (w *responseWriter) publish(end bool) error {
	msg := &nats.Msg{Subject: w.reply, Header: nats.Header{}}
	if !w.sent {
		for key, values := range w.header {
			if isProtocolHeader(key) {
				continue
			}
			for _, v := range values {
				msg.Header.Add(key, v)
			}
		}
		msg.Header.Set(HeaderStatus, strconv.Itoa(w.status))
	}
	msg.Header.Set(HeaderSeq, strconv.Itoa(w.seq))
	if end {
		msg.Header.Set(HeaderEnd, "1")
	}
	msg.Data = append([]byte(nil), w.buf.Bytes()...)
	w.buf.Reset()
	if err := w.nc.PublishMsg(msg); err != nil {
		w.err = err
		return err
	}
	w.sent = true
	w.seq++
	return nil
}

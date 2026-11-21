package natsapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

func startNATS(t *testing.T) *natsd.Server {
	t.Helper()
	srv, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatalf("nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server did not start")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func connect(t *testing.T, srv *natsd.Server) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// mintToken returns an account public key and a bearer token for a user in
// that account, the way the admin server issues them.
func mintToken(t *testing.T) (accountID, userPK, token string) {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ = kp.PublicKey()
	u, _, err := user.NewUser(&user.UserConfig{Email: "a@test.local", Password: "pw", AccountID: accountID}, kp)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := user.NewUserToken(u, 5, kp)
	if err != nil {
		t.Fatal(err)
	}
	return accountID, u.PublicKey, tok.String()
}

type echo struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	Query     string `json:"query"`
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
	Transport string `json:"transport"`
	Body      string `json:"body"`
	Remote    string `json:"remote"`
}

// newRouter builds a router wired like a real service: ID middleware, then
// Bearer on an echo handler and a streaming handler.
func newRouter(t *testing.T, trusted []string) *mux.Router {
	t.Helper()
	l := logger.NewLogger("natsapi", "test")
	r := mux.NewRouter()
	id := admin.NewID(admin.IDConfig{Logger: l})
	r.Use(id.Middleware)
	auth := admin.NewAuth(admin.AuthConfig{Logger: l, TrustedAccounts: trusted})
	r.HandleFunc("/api/v1/echo/{id}", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo-ID", mux.Vars(r)["id"])
		json.NewEncoder(w).Encode(echo{
			Method:    r.Method,
			Path:      r.URL.Path,
			Query:     r.URL.RawQuery,
			AccountID: admin.AccountIDFromContext(r.Context()),
			UserID:    admin.UserIDFromContext(r.Context()),
			Transport: r.Header.Get(natsapi.HeaderTransport),
			Body:      string(body),
			Remote:    r.RemoteAddr,
		})
	})).Methods("GET", "POST", "PUT", "DELETE")
	r.HandleFunc("/api/v1/stream", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 50; i++ {
			w.Write(bytes.Repeat([]byte{'a' + byte(i%26)}, 1000))
		}
	})).Methods("GET")
	r.HandleFunc("/api/v1/size", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Write([]byte(strings.Repeat("x", 0) + itoa(n)))
	})).Methods("POST")
	r.HandleFunc("/api/v1/panic", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})).Methods("GET")
	r.HandleFunc("/api/v1/created", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})).Methods("POST")
	return r
}

func itoa(n int64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + jsonNum(n)) }

func jsonNum(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type fixture struct {
	srv       *natsd.Server
	serverNC  *nats.Conn
	clientNC  *nats.Conn
	svc       *natsapi.Service
	client    *http.Client
	accountID string
	userPK    string
	token     string
}

func newFixture(t *testing.T, opts natsapi.Options) *fixture {
	t.Helper()
	f := &fixture{srv: startNATS(t)}
	f.accountID, f.userPK, f.token = mintToken(t)
	f.serverNC = connect(t, f.srv)
	f.clientNC = connect(t, f.srv)
	if opts.Service == "" {
		opts.Service = "test_service"
	}
	if opts.Handler == nil {
		opts.Handler = newRouter(t, []string{f.accountID})
	}
	svc, err := natsapi.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(f.accountID, f.serverNC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	f.svc = svc
	f.client = natsapi.NewClient(f.clientNC)
	return f
}

func (f *fixture) request(t *testing.T, method, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := natsapi.NewRequest(context.Background(), method, "test_service", path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	return req
}

func readEcho(t *testing.T, resp *http.Response) echo {
	t.Helper()
	defer resp.Body.Close()
	var e echo
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

func TestRoundTripAuthenticated(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	req := f.request(t, "POST", "/api/v1/echo/42?x=1", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-ID"); got != "42" {
		t.Errorf("response header X-Echo-ID = %q", got)
	}
	e := readEcho(t, resp)
	if e.Method != "POST" || e.Path != "/api/v1/echo/42" || e.Query != "x=1" {
		t.Errorf("request mismatch: %+v", e)
	}
	if e.AccountID != f.accountID {
		t.Errorf("account not pinned to the connection: %q", e.AccountID)
	}
	if e.UserID != f.userPK {
		t.Errorf("bearer did not resolve the user: %q", e.UserID)
	}
	if e.Transport != natsapi.TransportNATS || !strings.HasPrefix(e.Remote, "nats:") {
		t.Errorf("transport not marked: %+v", e)
	}
	if e.Body != `{"hello":"world"}` {
		t.Errorf("body %q", e.Body)
	}
}

func TestUnauthorizedPassesThrough(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	req, _ := natsapi.NewRequest(context.Background(), "GET", "test_service", "/api/v1/echo/1", nil)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	// A token from an untrusted account is also refused by the handler chain.
	_, _, other := mintToken(t)
	req, _ = natsapi.NewRequest(context.Background(), "GET", "test_service", "/api/v1/echo/1", nil)
	req.Header.Set("Authorization", "Bearer "+other)
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 for a foreign token", resp.StatusCode)
	}
}

func TestStatusCodesAndEmptyBodies(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	resp, err := f.client.Do(f.request(t, "POST", "/api/v1/created", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || len(body) != 0 {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	resp, err = f.client.Do(f.request(t, "GET", "/api/v1/missing", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404 from the router", resp.StatusCode)
	}
}

func TestStreamingResponseCrossesChunks(t *testing.T) {
	f := newFixture(t, natsapi.Options{ChunkSize: 4096})
	resp, err := f.client.Do(f.request(t, "GET", "/api/v1/stream", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 50000 {
		t.Fatalf("got %d bytes, want 50000", len(body))
	}
	for i := 0; i < 50; i++ {
		want := bytes.Repeat([]byte{'a' + byte(i%26)}, 1000)
		if !bytes.Equal(body[i*1000:(i+1)*1000], want) {
			t.Fatalf("chunk %d corrupted", i)
		}
	}
}

func TestLargeRequestBodyIsPulled(t *testing.T) {
	f := newFixture(t, natsapi.Options{ChunkSize: 4096})
	tr := f.client.Transport.(*natsapi.Transport)
	tr.ChunkSize = 4096
	payload := bytes.Repeat([]byte("0123456789"), 10000) // 100000 bytes
	resp, err := f.client.Do(f.request(t, "POST", "/api/v1/size", bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "100000" {
		t.Fatalf("service read %s bytes, want 100000", body)
	}
	// A body that fills the first chunk exactly must still end cleanly.
	resp, err = f.client.Do(f.request(t, "POST", "/api/v1/size", bytes.NewReader(payload[:4096])))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "4096" {
		t.Fatalf("service read %s bytes, want 4096", body)
	}
}

func TestAccountPinning(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	// Naming another account is refused before the handler runs.
	req := f.request(t, "GET", "/api/v1/echo/1", nil)
	req.Header.Set(natsapi.HeaderAccountID, "someone-else")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	// Naming the connection's own account is fine.
	req = f.request(t, "GET", "/api/v1/echo/1", nil)
	req.Header.Set(natsapi.HeaderAccountID, f.accountID)
	resp, err = f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if e := readEcho(t, resp); e.AccountID != f.accountID {
		t.Fatalf("account %q", e.AccountID)
	}
}

func TestBridgeRefusals(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	send := func(subject, path string) int {
		t.Helper()
		msg := &nats.Msg{Subject: subject, Header: nats.Header{}}
		if path != "" {
			msg.Header.Set(natsapi.HeaderPath, path)
		}
		msg.Header.Set("Authorization", "Bearer "+f.token)
		reply, err := f.clientNC.RequestMsg(msg, 2*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", subject, err)
		}
		var code int
		if _, err := fmtSscan(reply.Header.Get(natsapi.HeaderStatus), &code); err != nil {
			t.Fatalf("no status header on %s", subject)
		}
		return code
	}
	if got := send("api.test_service.GET.echo", ""); got != http.StatusBadRequest {
		t.Errorf("missing path: %d", got)
	}
	if got := send("api.test_service.GET.echo", "/api/v1/stream"); got != http.StatusBadRequest {
		t.Errorf("resource token mismatch: %d", got)
	}
	if got := send("api.test_service.GET._", "/index.html"); got != http.StatusNotFound {
		t.Errorf("path outside /api: %d", got)
	}
}

func fmtSscan(s string, v *int) (int, error) {
	n, err := json.Number(s).Int64()
	*v = int(n)
	return 1, err
}

func TestNoService(t *testing.T) {
	srv := startNATS(t)
	nc := connect(t, srv)
	client := natsapi.NewClient(nc)
	req, _ := natsapi.NewRequest(context.Background(), "GET", "nobody", "/api/v1/x", nil)
	_, err := client.Do(req)
	if !errors.Is(err, natsapi.ErrNoService) {
		t.Fatalf("err = %v, want ErrNoService", err)
	}
}

func TestHandlerPanicBecomes500(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	resp, err := f.client.Do(f.request(t, "GET", "/api/v1/panic", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", resp.StatusCode)
	}
	// The service keeps working afterwards.
	resp, err = f.client.Do(f.request(t, "GET", "/api/v1/echo/1", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d after panic", resp.StatusCode)
	}
}

func TestDiscovery(t *testing.T) {
	f := newFixture(t, natsapi.Options{Description: "echo over nats"})
	reply, err := f.clientNC.Request("$SRV.INFO.test_service", nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Name      string            `json:"name"`
		Metadata  map[string]string `json:"metadata"`
		Endpoints []struct {
			Name    string `json:"name"`
			Subject string `json:"subject"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(reply.Data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "test_service" || info.Metadata["account"] != f.accountID {
		t.Errorf("info %+v", info)
	}
	if len(info.Endpoints) != len(natsapi.Methods) {
		t.Fatalf("endpoints %d, want %d", len(info.Endpoints), len(natsapi.Methods))
	}
	subjects := map[string]bool{}
	for _, e := range info.Endpoints {
		subjects[e.Subject] = true
	}
	if !subjects["api.test_service.GET.>"] || !subjects["api.test_service.DELETE.>"] {
		t.Errorf("endpoint subjects %v", subjects)
	}
}

func TestServeManyConnections(t *testing.T) {
	f := newFixture(t, natsapi.Options{})
	// A second connection stands in for a second account on the same
	// server; requests on it are pinned to that account.
	second := connect(t, f.srv)
	if err := f.svc.Serve("second-account", second); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Serve("second-account", second); err != nil {
		t.Fatalf("serving twice should be a no-op: %v", err)
	}
	if n := len(f.svc.Accounts()); n != 2 {
		t.Fatalf("accounts %d", n)
	}
	if err := f.svc.Stop("second-account"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.svc.Accounts()); n != 1 {
		t.Fatalf("accounts after stop %d", n)
	}
	resp, err := f.client.Do(f.request(t, "GET", "/api/v1/echo/1", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSubjectAndResourceToken(t *testing.T) {
	cases := map[string]string{
		"/api/v1/users":                 "users",
		"/api/v1/users/42?x=1":          "users",
		"/api/v1":                       "_",
		"/api/v1/":                      "_",
		"/api/v2/things/a.b":            "things",
		"/api/a.b/c":                    "a_b",
		"/api/v1/accounts/me/apitokens": "accounts",
	}
	for path, want := range cases {
		if got := natsapi.ResourceToken(path); got != want {
			t.Errorf("ResourceToken(%q) = %q, want %q", path, got, want)
		}
	}
	subj, err := natsapi.Subject("admin_service", "get", "/api/v1/users/1")
	if err != nil || subj != "api.admin_service.GET.users" {
		t.Errorf("Subject = %q, %v", subj, err)
	}
	if _, err := natsapi.Subject("bad.name", "GET", "/api/v1/x"); err == nil {
		t.Error("invalid service name accepted")
	}
	if _, err := natsapi.Subject("ok", "GET", "/index.html"); err == nil {
		t.Error("path outside /api accepted")
	}
	if _, err := natsapi.New(natsapi.Options{Service: "x"}); err == nil {
		t.Error("missing handler accepted")
	}
}

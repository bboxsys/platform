package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/host"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/bboxsys/platform/user"
	"github.com/gorilla/mux"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// apiFixture is an embedded NATS server, an admin_service bridge in front of
// a bearer-protected router, and an agent config file whose credentials the
// router trusts.
type apiFixture struct {
	srv        *natsd.Server
	configFile string
}

func newAPIFixture(t *testing.T) *apiFixture {
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

	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := kp.PublicKey()
	u, _, err := user.NewUser(&user.UserConfig{Email: "agent@test.local", Password: "pw", AccountID: accountID}, kp)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := user.NewUserToken(u, 5, kp)
	if err != nil {
		t.Fatal(err)
	}

	l := logger.NewLogger("agent", "test")
	r := mux.NewRouter()
	id := admin.NewID(admin.IDConfig{Logger: l})
	r.Use(id.Middleware)
	auth := admin.NewAuth(admin.AuthConfig{Logger: l, TrustedAccounts: []string{accountID}})
	r.HandleFunc("/api/v1/users", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{{"email": "agent@test.local", "account": admin.AccountIDFromContext(r.Context())}})
	})).Methods("GET")
	r.HandleFunc("/api/v1/users", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"created":` + string(body) + `,"content_type":"` + r.Header.Get("Content-Type") + `"}`))
	})).Methods("POST")

	serverNC, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(serverNC.Close)
	svc, err := natsapi.New(natsapi.Options{Service: "admin_service", Handler: r, Description: "admin over nats"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(accountID, serverNC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	cfg := host.AgentConfig{
		AccountID:     accountID,
		HostID:        "host-1",
		JWT:           tok.String(),
		Seed:          "SU-not-used-by-the-test-connector",
		ControllerURL: srv.ClientURL(),
	}
	data, _ := json.Marshal(cfg)
	configFile := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &apiFixture{srv: srv, configFile: configFile}
}

// run executes the agent command with the test connector and returns what
// it wrote to stdout and stderr.
func (f *apiFixture) run(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root, a := newCommand(Options{Name: "test-agent"})
	a.connect = func(cfg *host.AgentConfig) (*nats.Conn, error) {
		if cfg.ControllerURL != f.srv.ClientURL() {
			t.Fatalf("connector got controller %q", cfg.ControllerURL)
		}
		return nats.Connect(cfg.ControllerURL)
	}
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--config", f.configFile}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestAPIGet(t *testing.T) {
	f := newAPIFixture(t)
	stdout, stderr, err := f.run(t, "", "api", "GET", "/api/v1/users")
	if err != nil {
		t.Fatalf("err %v, stderr %s", err, stderr)
	}
	if !strings.Contains(stderr, "200 OK") {
		t.Errorf("stderr %q", stderr)
	}
	var users []map[string]string
	if err := json.Unmarshal([]byte(stdout), &users); err != nil || len(users) != 1 {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if users[0]["account"] == "" {
		t.Errorf("handler saw no account: %v", users)
	}
}

func TestAPIPostWithData(t *testing.T) {
	f := newAPIFixture(t)
	stdout, stderr, err := f.run(t, "", "api", "post", "/api/v1/users", "--data", `{"email":"new@test.local"}`)
	if err != nil {
		t.Fatalf("err %v, stderr %s", err, stderr)
	}
	if !strings.Contains(stderr, "201 Created") {
		t.Errorf("stderr %q", stderr)
	}
	if !strings.Contains(stdout, `"created":{"email":"new@test.local"}`) || !strings.Contains(stdout, `"content_type":"application/json"`) {
		t.Errorf("stdout %q", stdout)
	}

	// --data - reads stdin; --data @file reads a file.
	stdout, _, err = f.run(t, `{"email":"stdin@test.local"}`, "api", "POST", "/api/v1/users", "--data", "-")
	if err != nil || !strings.Contains(stdout, "stdin@test.local") {
		t.Errorf("stdin body: err %v stdout %q", err, stdout)
	}
	file := filepath.Join(t.TempDir(), "body.json")
	os.WriteFile(file, []byte(`{"email":"file@test.local"}`), 0o600)
	stdout, _, err = f.run(t, "", "api", "POST", "/api/v1/users", "--data", "@"+file)
	if err != nil || !strings.Contains(stdout, "file@test.local") {
		t.Errorf("file body: err %v stdout %q", err, stdout)
	}
}

func TestAPIErrorStatusExitsNonZero(t *testing.T) {
	f := newAPIFixture(t)
	// A header override that drops the bearer gets a 401 from the router.
	_, stderr, err := f.run(t, "", "api", "GET", "/api/v1/users", "--header", "Authorization: Bearer not-a-token")
	if !errors.Is(err, errExit) {
		t.Fatalf("err = %v, want errExit; stderr %s", err, stderr)
	}
	if !strings.Contains(stderr, "401 Unauthorized") {
		t.Errorf("stderr %q", stderr)
	}
	// A route the service does not have is a 404.
	_, stderr, err = f.run(t, "", "api", "GET", "/api/v1/nothing")
	if !errors.Is(err, errExit) || !strings.Contains(stderr, "404") {
		t.Errorf("err %v stderr %q", err, stderr)
	}
	// No such service in the account.
	_, _, err = f.run(t, "", "api", "GET", "/api/v1/users", "--service", "nobody")
	if err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("missing service err = %v", err)
	}
	// Paths outside /api are refused before connecting.
	_, _, err = f.run(t, "", "api", "GET", "/index.html")
	if err == nil || !strings.Contains(err.Error(), "/api/") {
		t.Errorf("bad path err = %v", err)
	}
}

func TestServices(t *testing.T) {
	f := newAPIFixture(t)
	stdout, _, err := f.run(t, "", "services")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "admin_service 1.0.0 admin over nats") || !strings.Contains(stdout, "api.admin_service.GET.>") {
		t.Errorf("stdout %q", stdout)
	}
	stdout, _, err = f.run(t, "", "services", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var docs []map[string]any
	if err := json.Unmarshal([]byte(stdout), &docs); err != nil || len(docs) != 1 || docs[0]["name"] != "admin_service" {
		t.Errorf("json output %q: %v", stdout, err)
	}
}

func TestNewCommandHasAPIAndServices(t *testing.T) {
	root := NewCommand(Options{Name: "test-agent"})
	for _, name := range []string{"api", "services"} {
		sub, _, err := root.Find([]string{name})
		if err != nil || sub == nil || sub.Name() != name {
			t.Fatalf("subcommand %q not found: %v", name, err)
		}
	}
	api, _, _ := root.Find([]string{"api"})
	for _, flag := range []string{"data", "service", "header", "timeout"} {
		if api.Flags().Lookup(flag) == nil {
			t.Fatalf("api flag --%s missing", flag)
		}
	}
}

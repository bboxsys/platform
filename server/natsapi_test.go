package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	admin "github.com/bboxsys/platform/admin/client"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/natsapi"
	"github.com/bboxsys/platform/user"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

func testCert(t *testing.T) (keyPEM, certPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func startNATS(t *testing.T) *natsd.Server {
	t.Helper()
	srv, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, NoSigs: true, NoLog: true})
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func mintToken(t *testing.T) (accountID, userPK, token string) {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ = kp.PublicKey()
	u, _, err := user.NewUser(&user.UserConfig{Email: "s@test.local", Password: "pw", AccountID: accountID}, kp)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := user.NewUserToken(u, 5, kp)
	if err != nil {
		t.Fatal(err)
	}
	return accountID, u.PublicKey, tok.String()
}

// newNATSServer builds a Server with a messenger client on an embedded NATS
// server and one bearer-protected route that echoes its identity.
func newNATSServer(t *testing.T, srv *natsd.Server, accountID string, trusted []string) *Server {
	t.Helper()
	keyPEM, certPEM := testCert(t)
	s, err := NewServer(&ServerOptions{
		Application:        "unit_testing",
		Component:          "echo_service",
		PrivateKey:         keyPEM,
		Certificate:        certPEM,
		CA:                 certPEM,
		HTTPPort:           0,
		NATSAPI:            true,
		MessengerAccountID: accountID,
		TrustedAccounts:    trusted,
	})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := messenger.NewClientFromConn(connect(t, srv), "unit_testing", "echo_service")
	if err != nil {
		t.Fatal(err)
	}
	s.MessengerClient = mc
	id := admin.NewID(admin.IDConfig{Logger: s.Logger})
	s.Router.Use(id.Middleware)
	auth := admin.NewAuth(admin.AuthConfig{Logger: s.Logger, TrustedAccounts: trusted})
	s.Router.HandleFunc("/api/v1/whoami", auth.Bearer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"account":   admin.AccountIDFromContext(r.Context()),
			"user":      admin.UserIDFromContext(r.Context()),
			"transport": r.Header.Get(natsapi.HeaderTransport),
		})
	})).Methods("GET")
	t.Cleanup(s.Stop)
	return s
}

func TestStartNATSAPI(t *testing.T) {
	srv := startNATS(t)
	accountID, userPK, token := mintToken(t)
	s := newNATSServer(t, srv, accountID, []string{accountID})
	if err := s.StartNATSAPI(); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNATSAPI(); err != nil {
		t.Fatalf("second start should be a no-op: %v", err)
	}
	// Subscriptions register on the server asynchronously; flush before
	// the first request from another connection.
	if err := s.MessengerClient.Client.Conn.Flush(); err != nil {
		t.Fatal(err)
	}
	if s.NATSAPI == nil || s.NATSAPI.Name() != "echo_service" {
		t.Fatalf("service not created from Component")
	}

	client := natsapi.NewClient(connect(t, srv))
	req, _ := natsapi.NewRequest(context.Background(), "GET", "echo_service", "/api/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["account"] != accountID || got["user"] != userPK || got["transport"] != natsapi.TransportNATS {
		t.Fatalf("identity %v", got)
	}

	// Without a bearer the full handler chain still answers 401.
	req, _ = natsapi.NewRequest(context.Background(), "GET", "echo_service", "/api/v1/whoami", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}

	s.Stop()
	// Unsubscribes propagate to the server asynchronously.
	waitFor(t, "service to stop answering", func() bool {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err != nil
	})
}

func TestStartNATSAPIWithoutMessenger(t *testing.T) {
	keyPEM, certPEM := testCert(t)
	s, err := NewServer(&ServerOptions{Application: "unit_testing", Component: "x", PrivateKey: keyPEM, Certificate: certPEM, CA: certPEM})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartNATSAPI(); err == nil {
		t.Fatal("expected an error without a messenger client")
	}
}

type fakeProvider struct{ calls []string }

func (f *fakeProvider) ServiceCredentials(accountID, accountName string) (string, string, error) {
	f.calls = append(f.calls, accountID+"/"+accountName)
	return "jwt-" + accountID, "seed-" + accountID, nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func has(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestFollowAccounts(t *testing.T) {
	srv := startNATS(t)
	accountID, _, _ := mintToken(t)
	s := newNATSServer(t, srv, accountID, []string{accountID})
	if err := s.StartNATSAPI(); err != nil {
		t.Fatal(err)
	}
	s.MessengerClient.Client.Conn.Flush()
	provider := &fakeProvider{}
	var opened []string
	connector := func(id, jwt, seed string) (*nats.Conn, func(), error) {
		if jwt != "jwt-"+id || seed != "seed-"+id {
			t.Errorf("connector got credentials %q %q", jwt, seed)
		}
		nc, err := nats.Connect(srv.ClientURL())
		if err != nil {
			return nil, nil, err
		}
		opened = append(opened, id)
		return nc, nc.Close, nil
	}
	ac, err := s.FollowAccounts(provider, []AccountRef{
		{ID: accountID, Name: "primary"}, // skipped: served by StartNATSAPI
		{ID: "tenant-a", Name: "a"},
	}, WithAccountConnector(connector))
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close()
	if !has(ac.Accounts(), "tenant-a") || has(ac.Accounts(), accountID) {
		t.Fatalf("accounts %v", ac.Accounts())
	}
	if !has(s.NATSAPI.Accounts(), "tenant-a") || !has(s.NATSAPI.Accounts(), accountID) {
		t.Fatalf("served %v", s.NATSAPI.Accounts())
	}

	// A new tenant announced on the primary connection is followed.
	notifier := connect(t, srv)
	msg, _ := json.Marshal(admin.TenantCreatedNotification{AccountID: "tenant-b", AccountName: "b"})
	if err := notifier.Publish(admin.SubjectTenantCreated, msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "tenant-b to be served", func() bool { return has(s.NATSAPI.Accounts(), "tenant-b") })
	// Announcing it again does not open a second connection.
	notifier.Publish(admin.SubjectTenantCreated, msg)
	notifier.Flush()
	time.Sleep(50 * time.Millisecond)
	if n := len(ac.Accounts()); n != 2 {
		t.Fatalf("accounts %v", ac.Accounts())
	}

	// Deleting it stops the service and closes the connection.
	msg, _ = json.Marshal(admin.TenantDeletedNotification{AccountID: "tenant-b", AccountName: "b"})
	notifier.Publish(admin.SubjectTenantDeleted, msg)
	waitFor(t, "tenant-b to be dropped", func() bool { return !has(s.NATSAPI.Accounts(), "tenant-b") })
	if has(ac.Accounts(), "tenant-b") {
		t.Fatal("tenant-b still followed")
	}

	ac.Close()
	if n := len(ac.Accounts()); n != 0 {
		t.Fatalf("accounts after close %d", n)
	}
	if has(s.NATSAPI.Accounts(), "tenant-a") || !has(s.NATSAPI.Accounts(), accountID) {
		t.Fatalf("served after close %v", s.NATSAPI.Accounts())
	}
	if len(opened) != 2 {
		t.Fatalf("connections opened %v", opened)
	}
}

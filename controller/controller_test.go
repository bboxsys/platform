package controller

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bboxsys/platform/admin"
	"github.com/bboxsys/platform/config"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/platform/server"
	"github.com/bboxsys/platform/user"
	"github.com/nats-io/nats.go"
)

var (
	ADMIN_SERVER_PORT, _          = strconv.Atoi(os.Getenv("ADMIN_SERVER_PORT"))
	ADMIN_SERVER_URL              = os.Getenv("ADMIN_SERVER_URL")
	ADMIN_EMAIL                   = os.Getenv("ADMIN_EMAIL")
	ADMIN_PASSWORD                = os.Getenv("ADMIN_PASSWORD")
	CONTROLLER_PORT, _            = strconv.Atoi(os.Getenv("CONTROLLER_PORT"))
	CONTROLLER_MONITORING_PORT, _ = strconv.Atoi(os.Getenv("CONTROLLER_MONITORING_PORT"))
	CONTROLLER_URL                = os.Getenv("CONTROLLER_URL")
	NATS_PORT_CLIENT              = os.Getenv("NATS_PORT_CLIENT")
	ACCOUNT_SERVER_URL            = os.Getenv("ACCOUNT_SERVER_URL")
	OPERATOR_SERVICE_URL          = os.Getenv("OPERATOR_SERVICE_URL")
	KEY_FILE                      = os.Getenv("KEY_FILE")
	CERT_FILE                     = os.Getenv("CERT_FILE")
	CA_FILE                       = os.Getenv("CA_FILE")
	INITIAL_PASSWORD              = os.Getenv("INITIAL_PASSWORD")
)

func TestAll(t *testing.T) {
	// TestAll is a full integration test: it boots an admin server with TLS
	// material from KEY_FILE/CERT_FILE/CA_FILE, talks to NATS, and drives the
	// controller over HTTP. It only runs when that environment is configured;
	// otherwise skip so the package still builds and unit-tests in CI.
	if os.Getenv("KEY_FILE") == "" || os.Getenv("CERT_FILE") == "" || os.Getenv("CA_FILE") == "" {
		t.Skip("set KEY_FILE, CERT_FILE, CA_FILE (and the ADMIN_*/NATS_* vars) to run the controller integration test")
	}
	var wg sync.WaitGroup
	keyBytes, err := ioutil.ReadFile(os.Getenv("KEY_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	certBytes, err := ioutil.ReadFile(os.Getenv("CERT_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	caBytes, err := ioutil.ReadFile(os.Getenv("CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.New("test")
	storeLocation := fmt.Sprintf("%v/%v", c.DefaultDataDir(), "admin.db")
	adminServer, err := admin.NewAdminServer(&server.ServerOptions{
		Application: "unit_testing",
		Component:   "admin_server",
		PrivateKey:  keyBytes,
		Certificate: certBytes,
		CA:          caBytes,
		HTTPPort:    ADMIN_SERVER_PORT,
		Messenger:   false,
		Metadata: map[string]interface{}{
			"storeLocation":        storeLocation,
			"tokenValidMinutes":    60,
			"defaultAdminEmail":    ADMIN_EMAIL,
			"defaultAdminPassword": ADMIN_PASSWORD,
			"accountServerURL":     ACCOUNT_SERVER_URL,
			"operatorServiceURL":   OPERATOR_SERVICE_URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(storeLocation)
	go adminServer.Start()

	fmt.Printf("pausing for 3 seconds to allow admin server to start\n")
	time.Sleep(3000 * time.Millisecond)

	systemAccountID, err := adminServer.GetSysAccountID()
	if err != nil {
		t.Fatal(err)
	}
	operatorJWT, err := adminServer.GetOperatorJWT()
	if err != nil {
		t.Fatal(err)
	}
	controllerConfig := ControllerConfig{
		Port:            CONTROLLER_PORT,
		MonitorPort:     CONTROLLER_MONITORING_PORT,
		KeyFile:         KEY_FILE,
		CertFile:        CERT_FILE,
		CAFile:          CA_FILE,
		OperatorJWT:     operatorJWT,
		SystemAccountID: systemAccountID,
		ResolverURL:     ACCOUNT_SERVER_URL,
	}
	fmt.Printf("controllerConfig:\n%#v\n", controllerConfig)
	controller := NewController(controllerConfig)
	controller.Start()
	wg.Add(1)
	fmt.Println("sleeping 3 seconds for controller to start")
	time.Sleep(3 * time.Second)

	// API client
	transConfig := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: transConfig}

	// create account
	accountJSON, err := json.Marshal(map[string]string{
		"name":     "acme",
		"admin_id": adminServer.AdminAccountID(),
	})
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/accounts", ADMIN_SERVER_URL), bytes.NewReader(accountJSON))
	if err != nil {
		t.Errorf("error creating new request : %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(ADMIN_EMAIL, ADMIN_PASSWORD)
	req.Header.Set("X-Blackbox-AccountID", adminServer.AdminAccountID())
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("error creating new account: %v", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	var account user.Account
	err = json.Unmarshal(body, &account)
	if err != nil {
		t.Errorf("error unmarshalling new account: %v", err)
	}
	fmt.Printf("successfully created accountID: %s \n", account.ID)

	// create token for admin user
	adminTokenJSON, err := json.Marshal(map[string]string{
		"name":        "admintoken",
		"description": "testdescription",
	})
	if err != nil {
		t.Errorf("error marshaling CreateToken payload for admin user: %v", err)
		return
	}
	defaultAdminID, _, err := adminServer.GetUserIDByEmail(ADMIN_EMAIL)
	if err != nil {
		t.Errorf("error getting defaul admin id by email: %v", err)
		return
	}
	req, err = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/users/%v/token", ADMIN_SERVER_URL, defaultAdminID), bytes.NewReader(adminTokenJSON))
	req.SetBasicAuth(ADMIN_EMAIL, ADMIN_PASSWORD)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Blackbox-AccountID", adminServer.AdminAccountID())
	resp, err = client.Do(req)
	if err != nil {
		t.Errorf("error logging out user: %v", err)
		return
	}
	body, err = ioutil.ReadAll(resp.Body)
	adminToken := user.Token{}
	err = json.Unmarshal(body, &adminToken)
	if err != nil {
		t.Errorf("error unmarshaling create user token response body: %v", err)
		return
	}
	fmt.Printf("successfully created admin user token: %s\n", adminToken.String())

	// Create User1
	user1JSON, err := json.Marshal(map[string]string{
		"firstname":  "Dak",
		"lastname":   "Prescott",
		"email":      "dak@cowyboys.com",
		"password":   "cowboysrock",
		"account_id": account.ID,
	})
	if err != nil {
		t.Errorf("error marshaling user1: %v", err)
	}
	req, err = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/users", ADMIN_SERVER_URL), bytes.NewReader(user1JSON))
	if err != nil {
		t.Errorf("error creating new request: %v", err)
	}
	authHeader := fmt.Sprintf("Bearer %v", adminToken.String())
	req.Header.Set("X-Blackbox-AccountID", adminServer.AdminAccountID())
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Errorf("error creating new user1: %v", err)
	}
	body, err = ioutil.ReadAll(resp.Body)
	ur1 := user.User{}
	err = json.Unmarshal(body, &ur1)
	if err != nil {
		t.Errorf("error unmarshaling new user1: %v", err)
	}
	// Create User2
	user2JSON, err := json.Marshal(map[string]string{
		"firstname":  "Amari",
		"lastname":   "Cooper",
		"email":      "amari@cowyboys.com",
		"password":   "cowboysrock",
		"account_id": account.ID,
	})
	if err != nil {
		t.Errorf("error marshaling user2: %v", err)
	}
	req, err = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/users", ADMIN_SERVER_URL), bytes.NewReader(user2JSON))
	if err != nil {
		t.Errorf("error creating new request: %v", err)
	}
	authHeader = fmt.Sprintf("Bearer %v", adminToken.String())
	req.Header.Set("X-Blackbox-AccountID", adminServer.AdminAccountID())
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Errorf("error creating new user2: %v", err)
	}
	body, err = ioutil.ReadAll(resp.Body)
	ur2 := user.User{}
	err = json.Unmarshal(body, &ur2)
	if err != nil {
		t.Errorf("error unmarshaling new user2: %v", err)
	}

	certPair, err := tls.LoadX509KeyPair(CERT_FILE, KEY_FILE)
	if err != nil {
		t.Fatalf("error parsing X509 certificate/key pair for identity cert: %v", err)
	}
	pemBytes, err := ioutil.ReadFile(CA_FILE)
	if err != nil {
		t.Fatalf("error parsing CA cert file: %v\n", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certPair},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}

	nc1, err := nats.Connect(CONTROLLER_URL, ur1.NATSCreds(ur1.Seed), nats.Secure(tlsConfig))

	if err != nil {
		t.Fatalf("error creating client1: %v\n", err)
	}
	defer nc1.Close()
	nc1.Subscribe("foo", func(m *nats.Msg) {
		fmt.Printf("Received a message: %s\n", string(m.Data))
	})
	nc2, err := nats.Connect(CONTROLLER_URL, ur2.NATSCreds(ur2.Seed), nats.Secure(tlsConfig))
	if err != nil {
		t.Fatalf("error creating client2: %v\n", err)
	}
	defer nc2.Close()
	nc2.Publish("foo", []byte("Hello World"))
	fmt.Println("sleeping 3 seconds to receive message")
	time.Sleep(3 * time.Second)

	//////////////////////////////////////////////////////////////////////
	// Publisher
	//////////////////////////////////////////////////////////////////////
	mc_pub, err := messenger.NewClient(&messenger.ClientOptions{
		Application:   "acme",
		Component:     "mc_pub",
		PrivateKey:    keyBytes,
		Certificate:   certBytes,
		CA:            caBytes,
		JWT:           ur1.Token,
		Seed:          ur1.Seed,
		ControllerURL: CONTROLLER_URL,
	})
	if err != nil {
		t.Fatalf("error creating publisher: %v\n", err)
	}
	//////////////////////////////////////////////////////////////////////
	// Subscriber
	//////////////////////////////////////////////////////////////////////
	mc_sub, err := messenger.NewClient(&messenger.ClientOptions{
		Application:   "acme",
		Component:     "mc_sub",
		PrivateKey:    keyBytes,
		Certificate:   certBytes,
		CA:            caBytes,
		JWT:           ur2.Token,
		Seed:          ur2.Seed,
		ControllerURL: CONTROLLER_URL,
	})
	if err != nil {
		t.Fatalf("error creating subscriber: %v\n", err)
	}
	sub := "foo"
	defer mc_pub.Close()
	defer mc_sub.Close()

	//////////////////////////////////////////////////////////////////////
	// Test
	//////////////////////////////////////////////////////////////////////
	mc_sub.Subscribe(sub, func(obj interface{}) {
		fmt.Printf("received message: %#v\n", obj)
	})
	mc_sub.RegisterWorker("finance", func(job interface{}) {
		fmt.Printf("received queue message: %#v\n", job)
	})
	test := struct {
		Name string
	}{"daddy"}
	fmt.Printf("publishing message: %#v\n", &test)
	mc_pub.Publish(sub, &test)
	job := struct {
		name    string
		payload interface{}
	}{"get_transactions", "accountnumber"}
	mc_pub.Publish("finance", &job)
	time.Sleep(1 * time.Second)
}

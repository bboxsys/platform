package messenger

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

var (
	QUEUE_GROUP = "workers"
)

type ClientOptions struct {
	Application string `json:"application"`
	Component   string `json:"component"`
	// can get the account id from the JWT, but adding
	// here for convenience
	AccountID     string `json:"account_id"`
	PrivateKey    []byte `json:"private_key"`
	Certificate   []byte `json:"certificate"`
	CA            []byte `json:"ca"`
	JWT           string `json:"jwt"`
	Seed          string `json:"seed"`
	ControllerURL string `json:"controller_url"`
}

type Client struct {
	o         *ClientOptions
	l         *logger.Logger
	AccountID string
	Client    *nats.EncodedConn
}

func NewClient(options *ClientOptions) (*Client, error) {
	client := Client{
		o:         options,
		l:         logger.NewLogger(options.Application, options.Component),
		AccountID: options.AccountID,
	}
	err := client.Connect()
	return &client, err

}

func (options *ClientOptions) GetCertificate() (tls.Certificate, error) {
	certificate, err := tls.X509KeyPair(options.Certificate, options.PrivateKey)
	return certificate, err
}

func (options *ClientOptions) GetTLSConfig() (*tls.Config, error) {
	tlsConfig := tls.Config{}
	certificate, err := options.GetCertificate()
	if err != nil {
		return &tlsConfig, err
	}
	tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(options.CA)
	tlsConfig.RootCAs = pool
	tlsConfig.MinVersion = tls.VersionTLS12

	u, err := url.Parse(options.ControllerURL)
	if err != nil {
		return &tlsConfig, err
	}
	if u.Host != "" {
		host, _, _ := net.SplitHostPort(u.Host)
		tlsConfig.ServerName = host
	} else {
		tlsConfig.ServerName = "localhost"
	}

	return &tlsConfig, nil
}

func (mc *Client) Connect() error {
	options := make([]nats.Option, 0)
	totalWait := 10 * time.Minute
	reconnectDelay := time.Second
	options = append(options, nats.Name(fmt.Sprintf("%s_%s", mc.o.Application, mc.o.Component)))
	options = append(options, nats.RetryOnFailedConnect(true))
	options = append(options, nats.ReconnectWait(totalWait))
	options = append(options, nats.MaxReconnects(int(totalWait/reconnectDelay)))
	options = append(options, nats.ReconnectHandler(func(_ *nats.Conn) {
		mc.l.Info("attempting to connect for 10 minutes")
	}))

	tls, err := mc.o.GetTLSConfig()
	if err != nil {
		return err
	}
	options = append(options, nats.Secure(tls))
	options = append(options, mc.NATSCreds())
	nc, err := nats.Connect(mc.o.ControllerURL, options...)
	if err != nil {
		mc.l.Fatal(fmt.Sprintf("messenger_client: unexpected error on connect: %v", err))
		return err
	}
	c, err := nats.NewEncodedConn(nc, nats.JSON_ENCODER)
	if err != nil {
		mc.l.Fatal(fmt.Sprintf("messenger_client: unexpected error on connect with encoder: %v", err))
		return err
	}
	mc.Client = c
	return nil
}

func (mc *Client) Publish(subject string, msg interface{}) {
	err := mc.Client.Publish(subject, msg)
	if err != nil {
		mc.l.ErrorE().Str("subject", subject).Err(err).Msg("messenger_client: error publishing message to subject")
	}
	return
}

// PublishRaw publishes raw bytes to a NATS subject.
func (mc *Client) PublishRaw(subject string, data []byte) error {
	if mc.Client == nil || mc.Client.Conn == nil {
		return fmt.Errorf("not connected")
	}
	return mc.Client.Conn.Publish(subject, data)
}

// IsConnected returns true if the NATS connection is established and not closed.
func (mc *Client) IsConnected() bool {
	if mc.Client == nil || mc.Client.Conn == nil {
		return false
	}
	return mc.Client.Conn.IsConnected()
}

// SubscribeRaw subscribes to a NATS subject and receives raw byte messages.
func (mc *Client) SubscribeRaw(subject string, cb func(data []byte)) {
	_, err := mc.Client.Conn.Subscribe(subject, func(msg *nats.Msg) {
		cb(msg.Data)
	})
	if err != nil {
		mc.l.ErrorE().Str("subject", subject).Err(err).Msg("messenger_client: error subscribing raw to subject")
	}
}

func (mc *Client) Close() {
	mc.l.Info("messenger_client: closing messenger client")
	mc.Client.Close()
	return
}

func (mc *Client) Subscribe(subject string, cb func(obj interface{})) {
	_, err := mc.Client.Subscribe(subject, cb)
	if err != nil {
		mc.l.ErrorE().Str("subject", subject).Err(err).Msg("messenger_client: error subscribing to subject")
	}
	return
}

func (mc *Client) UnSubscribe() {
}

func (mc *Client) RegisterWorker(subject string, cb func(obj interface{})) {
	mc.Client.QueueSubscribe(subject, QUEUE_GROUP, cb)
	return
}

func (mc *Client) NATSCreds() nats.Option {
	return func(o *nats.Options) error {
		userCB := func() (string, error) {
			return mc.o.JWT, nil
		}
		sigCB := func(nonce []byte) ([]byte, error) {
			kp, err := nkeys.FromSeed([]byte(mc.o.Seed))
			if err != nil {
				return nil, err
			}
			sig, _ := kp.Sign(nonce)
			return sig, nil
		}
		o.UserJWT = userCB
		o.SignatureCB = sigCB
		return nil
	}
}

// NewClientFromConn wraps an already established NATS connection, for a
// process that runs its own embedded server or a test that does not need the
// mTLS and JWT handshake of NewClient. Closing the client closes nc.
func NewClientFromConn(nc *nats.Conn, application, component string) (*Client, error) {
	c, err := nats.NewEncodedConn(nc, nats.JSON_ENCODER)
	if err != nil {
		return nil, err
	}
	return &Client{
		o:      &ClientOptions{Application: application, Component: component},
		l:      logger.NewLogger(application, component),
		Client: c,
	}, nil
}

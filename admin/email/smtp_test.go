package email

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/smtp"
	"strings"
	"testing"

	"github.com/bboxsys/platform/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSMTP struct {
	gotAddr string
	gotAuth smtp.Auth
	gotFrom string
	gotTo   []string
	gotMsg  []byte
	err     error
}

func (f *fakeSMTP) send(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	f.gotAddr = addr
	f.gotAuth = auth
	f.gotFrom = from
	f.gotTo = to
	f.gotMsg = msg
	return f.err
}

func newTestSMTPSender(t *testing.T, fake *fakeSMTP) *SMTPSender {
	t.Helper()
	return &SMTPSender{
		cfg: SMTPConfig{
			Host:            "smtp.gmail.com",
			Port:            587,
			Username:        "mailer@example.com",
			Password:        "app-password",
			FromEmail:       "invites@example.com",
			FromName:        "Acme",
			InviteAcceptURL: "https://app.example.com/invite/accept",
			AppDisplayName:  "Acme",
		},
		log:      logger.NewLogger("test", "email"),
		sendMail: fake.send,
	}
}

func TestSMTPSender_SendInvitation_Success(t *testing.T) {
	fake := &fakeSMTP{}
	s := newTestSMTPSender(t, fake)

	err := s.SendInvitation(context.Background(), testInvitation(), "admin@example.com")
	require.NoError(t, err)

	assert.Equal(t, "smtp.gmail.com:587", fake.gotAddr)
	assert.NotNil(t, fake.gotAuth)
	assert.Equal(t, "invites@example.com", fake.gotFrom)
	assert.Equal(t, []string{"invitee@example.com"}, fake.gotTo)

	msg := string(fake.gotMsg)
	assert.Contains(t, msg, "From: Acme <invites@example.com>\r\n")
	assert.Contains(t, msg, "To: invitee@example.com\r\n")
	assert.Contains(t, msg, "Subject: You've been invited to Acme\r\n")
	assert.Contains(t, msg, "MIME-Version: 1.0\r\n")
	assert.Contains(t, msg, "Content-Type: multipart/alternative;")
}

func TestSMTPSender_NonASCIIFromNameIsQEncoded(t *testing.T) {
	fake := &fakeSMTP{}
	s := newTestSMTPSender(t, fake)
	s.cfg.FromName = "BläckBox"

	require.NoError(t, s.SendInvitation(context.Background(), testInvitation(), ""))
	assert.Contains(t, string(fake.gotMsg), "=?utf-8?q?")
}

func TestSMTPSender_SendInvitation_NoFromName(t *testing.T) {
	fake := &fakeSMTP{}
	s := newTestSMTPSender(t, fake)
	s.cfg.FromName = ""

	err := s.SendInvitation(context.Background(), testInvitation(), "")
	require.NoError(t, err)

	assert.Contains(t, string(fake.gotMsg), "From: invites@example.com\r\n")
}

func TestSMTPSender_SendInvitation_SendError(t *testing.T) {
	fake := &fakeSMTP{err: errors.New("535 5.7.8 username and password not accepted")}
	s := newTestSMTPSender(t, fake)

	err := s.SendInvitation(context.Background(), testInvitation(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smtp send")
	assert.Contains(t, err.Error(), "535")
}

func TestSMTPSender_MIMEBodyIsParseable(t *testing.T) {
	fake := &fakeSMTP{}
	s := newTestSMTPSender(t, fake)
	require.NoError(t, s.SendInvitation(context.Background(), testInvitation(), "admin@example.com"))

	msg := string(fake.gotMsg)

	headerEnd := strings.Index(msg, "\r\n\r\n")
	require.Greater(t, headerEnd, 0)
	header := msg[:headerEnd]
	body := msg[headerEnd+4:]

	ctLine := ""
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Content-Type:") {
			ctLine = strings.TrimSpace(strings.TrimPrefix(line, "Content-Type:"))
			break
		}
	}
	require.NotEmpty(t, ctLine)

	mediaType, params, err := mime.ParseMediaType(ctLine)
	require.NoError(t, err)
	assert.Equal(t, "multipart/alternative", mediaType)
	boundary := params["boundary"]
	require.NotEmpty(t, boundary)

	mr := multipart.NewReader(strings.NewReader(body), boundary)
	parts := map[string]string{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		ct := p.Header.Get("Content-Type")
		decoded, err := io.ReadAll(quotedprintable.NewReader(p))
		require.NoError(t, err)
		parts[ct] = string(decoded)
	}

	require.Contains(t, parts, `text/plain; charset="UTF-8"`)
	require.Contains(t, parts, `text/html; charset="UTF-8"`)

	textBody := parts[`text/plain; charset="UTF-8"`]
	htmlBody := parts[`text/html; charset="UTF-8"`]
	assert.Contains(t, textBody, "https://app.example.com/invite/accept?token=tok-abc")
	assert.Contains(t, htmlBody, "https://app.example.com/invite/accept?token=tok-abc")
	assert.Contains(t, textBody, "admin@example.com")
}

func TestNewSMTPSender_ValidationErrors(t *testing.T) {
	log := logger.NewLogger("test", "email")
	base := SMTPConfig{
		Host:            "smtp.gmail.com",
		Username:        "u",
		Password:        "p",
		FromEmail:       "from@example.com",
		InviteAcceptURL: "https://example.com/accept",
	}

	cases := []struct {
		name    string
		mutate  func(c *SMTPConfig)
		wantErr string
	}{
		{"missing host", func(c *SMTPConfig) { c.Host = "" }, "host"},
		{"missing username", func(c *SMTPConfig) { c.Username = "" }, "username"},
		{"missing password", func(c *SMTPConfig) { c.Password = "" }, "password"},
		{"missing from", func(c *SMTPConfig) { c.FromEmail = "" }, "from email"},
		{"missing accept url", func(c *SMTPConfig) { c.InviteAcceptURL = "" }, "accept URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			_, err := NewSMTPSender(cfg, log)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestNewSMTPSender_DefaultsPort(t *testing.T) {
	log := logger.NewLogger("test", "email")
	s, err := NewSMTPSender(SMTPConfig{
		Host:            "smtp.gmail.com",
		Username:        "u",
		Password:        "p",
		FromEmail:       "from@example.com",
		InviteAcceptURL: "https://example.com/accept",
	}, log)
	require.NoError(t, err)
	assert.Equal(t, 587, s.cfg.Port)
}

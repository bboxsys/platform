package email

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/smtp"
	"net/textproto"
	"strings"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
)

// SMTPConfig holds the parameters needed to construct an SMTPSender.
// Designed for authenticated SMTP submission (e.g. Google Workspace via
// smtp.gmail.com:587 with an app password) but works with any provider
// that speaks STARTTLS + PLAIN auth on the submission port.
type SMTPConfig struct {
	Host            string // smtp.gmail.com
	Port            int    // 587
	Username        string // mailer@example.com (auth identity)
	Password        string // Google app password (16 chars, no spaces)
	FromEmail       string // invites@example.com (send-as alias or auth user)
	FromName        string // optional display name shown in mail clients
	InviteAcceptURL string
	AppDisplayName  string
}

// sendMailFunc matches smtp.SendMail's signature so tests can substitute
// a fake without dialing a real server.
type sendMailFunc func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error

// SMTPSender delivers email via authenticated SMTP submission.
type SMTPSender struct {
	cfg      SMTPConfig
	log      *logger.Logger
	sendMail sendMailFunc
}

func (s *SMTPSender) appName() string {
	if s.cfg.AppDisplayName == "" {
		return "BlackBox"
	}
	return s.cfg.AppDisplayName
}

// NewSMTPSender constructs an SMTPSender. Returns an error if required
// configuration is missing.
func NewSMTPSender(cfg SMTPConfig, log *logger.Logger) (*SMTPSender, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("smtp: host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, fmt.Errorf("smtp: username and password are required")
	}
	if cfg.FromEmail == "" {
		return nil, fmt.Errorf("smtp: from email is required")
	}
	if cfg.InviteAcceptURL == "" {
		return nil, fmt.Errorf("smtp: invite accept URL is required")
	}
	return &SMTPSender{
		cfg:      cfg,
		log:      log,
		sendMail: smtp.SendMail,
	}, nil
}

// SendInvitation sends an invitation email via SMTP submission.
func (s *SMTPSender) SendInvitation(_ context.Context, inv *user.Invitation, inviterEmail string) error {
	link, err := buildAcceptLink(s.cfg.InviteAcceptURL, inv.Token)
	if err != nil {
		return fmt.Errorf("build accept link: %w", err)
	}

	subject := fmt.Sprintf("You've been invited to %s", s.appName())
	textBody := renderText(s.appName(), link, inv, inviterEmail)
	htmlBody := renderHTML(s.appName(), link, inv, inviterEmail)

	msg, err := buildMIMEMessage(formatAddress(s.cfg.FromName, s.cfg.FromEmail), inv.Email, subject, textBody, htmlBody)
	if err != nil {
		return fmt.Errorf("build mime message: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	if err := s.sendMail(addr, auth, s.cfg.FromEmail, []string{inv.Email}, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// formatAddress returns a header-ready address. When name is non-empty
// the result is "Name <email>" with the name properly RFC 2047 encoded;
// otherwise it's the bare email.
func formatAddress(name, email string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", name), email)
}

// buildMIMEMessage assembles a multipart/alternative message with both
// plain-text and HTML parts. Both parts are quoted-printable encoded so
// long HTML lines stay within SMTP's 998-char line limit.
func buildMIMEMessage(from, to, subject, textBody, htmlBody string) ([]byte, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	textHeader := textproto.MIMEHeader{}
	textHeader.Set("Content-Type", `text/plain; charset="UTF-8"`)
	textHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	textPart, err := mw.CreatePart(textHeader)
	if err != nil {
		return nil, err
	}
	textQP := quotedprintable.NewWriter(textPart)
	if _, err := textQP.Write([]byte(textBody)); err != nil {
		return nil, err
	}
	if err := textQP.Close(); err != nil {
		return nil, err
	}

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", `text/html; charset="UTF-8"`)
	htmlHeader.Set("Content-Transfer-Encoding", "quoted-printable")
	htmlPart, err := mw.CreatePart(htmlHeader)
	if err != nil {
		return nil, err
	}
	htmlQP := quotedprintable.NewWriter(htmlPart)
	if _, err := htmlQP.Write([]byte(htmlBody)); err != nil {
		return nil, err
	}
	if err := htmlQP.Close(); err != nil {
		return nil, err
	}

	if err := mw.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n", mw.Boundary())
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())

	return msg.Bytes(), nil
}

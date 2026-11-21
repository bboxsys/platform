// Package email provides outbound email delivery for the admin service.
//
// The Sender interface lets the admin server stay decoupled from the
// underlying provider. SESSender talks to Amazon SES; NoopSender is
// used in development environments where SES is not configured.
package email

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/bboxsys/platform/user"
)

// Sender delivers transactional emails on behalf of the admin service.
type Sender interface {
	SendInvitation(ctx context.Context, inv *user.Invitation, inviterEmail string) error
}

// buildAcceptLink composes the accept URL by appending the token as a
// query parameter, preserving any existing query string on the base URL.
func buildAcceptLink(base, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func renderText(appName, link string, inv *user.Invitation, inviterEmail string) string {
	from := "an administrator"
	if inviterEmail != "" {
		from = inviterEmail
	}
	return fmt.Sprintf(
		"You've been invited to %s by %s.\n\n"+
			"Accept your invitation by visiting:\n%s\n\n"+
			"This invitation expires on %s.\n\n"+
			"If you weren't expecting this email, you can safely ignore it.\n",
		appName, from, link, inv.ExpiresAt.UTC().Format(time.RFC1123),
	)
}

func renderHTML(appName, link string, inv *user.Invitation, inviterEmail string) string {
	from := "an administrator"
	if inviterEmail != "" {
		from = inviterEmail
	}
	return fmt.Sprintf(`<!doctype html>
<html>
  <body style="font-family: -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif; color: #222; max-width: 560px; margin: 0 auto; padding: 24px;">
    <h2 style="margin-top: 0;">You've been invited to %s</h2>
    <p>%s has invited you to join %s.</p>
    <p style="margin: 32px 0;">
      <a href="%s" style="background:#1976d2;color:#fff;padding:12px 20px;border-radius:4px;text-decoration:none;display:inline-block;">Accept invitation</a>
    </p>
    <p style="font-size: 13px; color: #666;">Or paste this link into your browser:<br><a href="%s">%s</a></p>
    <p style="font-size: 13px; color: #666;">This invitation expires on %s.</p>
    <hr style="border:none;border-top:1px solid #eee;margin:24px 0;">
    <p style="font-size: 12px; color: #888;">If you weren't expecting this email, you can safely ignore it.</p>
  </body>
</html>`, appName, from, appName, link, link, link, inv.ExpiresAt.UTC().Format(time.RFC1123))
}

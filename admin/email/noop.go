package email

import (
	"context"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
)

// NoopSender satisfies Sender without delivering anything. It logs that an
// invitation was created (id + email) so developers running without an email
// provider know one is pending; the token itself is a bearer secret and is
// retrieved via the invitations API, not the logs.
type NoopSender struct {
	log *logger.Logger
}

func NewNoopSender(log *logger.Logger) *NoopSender {
	return &NoopSender{log: log}
}

func (n *NoopSender) SendInvitation(_ context.Context, inv *user.Invitation, _ string) error {
	if n.log != nil {
		// Never log inv.Token: it is a bearer secret and this log is persisted
		// to the queryable admin log store. Log the id/email only; fetch the
		// token from GET /api/v1/invitations when running without a provider.
		n.log.InfoE().Str("invitationID", inv.ID).Str("email", inv.Email).Msg("email: noop sender (no provider configured)")
	}
	return nil
}

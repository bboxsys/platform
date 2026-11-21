package email

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testInvitation() *user.Invitation {
	return &user.Invitation{
		ID:        "inv-id",
		Token:     "tok-abc",
		AccountID: "acct-1",
		Email:     "invitee@example.com",
		Roles:     []string{"user"},
		Status:    user.InvitationPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(72 * time.Hour),
	}
}

func TestBuildAcceptLink_PreservesExistingQuery(t *testing.T) {
	got, err := buildAcceptLink("https://app.example.com/invite/accept?ref=email", "tok-xyz")
	require.NoError(t, err)
	assert.True(t, strings.Contains(got, "ref=email"))
	assert.True(t, strings.Contains(got, "token=tok-xyz"))
}

func TestNoopSender_DoesNotPanicOrError(t *testing.T) {
	s := NewNoopSender(logger.NewLogger("test", "email"))
	err := s.SendInvitation(context.Background(), testInvitation(), "")
	assert.NoError(t, err)
}

package user

import (
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

func accountKey(t *testing.T) nkeys.KeyPair {
	t.Helper()
	kp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func decode(t *testing.T, token string) *jwt.UserClaims {
	t.Helper()
	claims, err := jwt.DecodeUserClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestAPIPublishSubjects(t *testing.T) {
	all := APIPublishSubjects("admin_service")
	if len(all) != len(APIMethods) {
		t.Fatalf("got %d subjects, want %d", len(all), len(APIMethods))
	}
	if all[0] != "api.admin_service.GET.>" {
		t.Errorf("first subject %q", all[0])
	}
	some := APIPublishSubjects("media_service", "GET", "POST")
	if len(some) != 2 || some[1] != "api.media_service.POST.>" {
		t.Errorf("subjects %v", some)
	}
}

func TestReadOnlyAPIPermissions(t *testing.T) {
	p := ReadOnlyAPIPermissions("admin_service", "media_service")
	want := map[string]bool{
		"_INBOX.>":                 true,
		"$SRV.>":                   true,
		"sys.log.>":                true,
		"sys.metrics.>":            true,
		"sys.ai.>":                 true,
		"api.admin_service.GET.>":  true,
		"api.admin_service.HEAD.>": true,
		"api.media_service.GET.>":  true,
		"api.media_service.HEAD.>": true,
	}
	if len(p.PublishAllow) != len(want) {
		t.Fatalf("allow %v", p.PublishAllow)
	}
	for _, s := range p.PublishAllow {
		if !want[s] {
			t.Errorf("unexpected subject %q", s)
		}
	}
	if len(p.SubscribeAllow) != 0 || len(p.PublishDeny) != 0 {
		t.Errorf("read-only permissions should only set a publish allow list: %+v", p)
	}
	if p.IsEmpty() || (*NATSPermissions)(nil).IsEmpty() == false || (&NATSPermissions{}).IsEmpty() == false {
		t.Error("IsEmpty wrong")
	}
}

func TestPermissionsWrittenIntoJWT(t *testing.T) {
	kp := accountKey(t)
	accountID, _ := kp.PublicKey()

	// Unrestricted identities stay unrestricted.
	svc, err := NewService(&UserConfig{Name: "plain", AccountID: accountID}, kp)
	if err != nil {
		t.Fatal(err)
	}
	if c := decode(t, svc.Token); len(c.Permissions.Pub.Allow) != 0 || len(c.Permissions.Sub.Allow) != 0 {
		t.Errorf("unrestricted service got permissions: %+v", c.Permissions)
	}
	if PermissionsFromClaims(svc.Claims) != nil {
		t.Error("PermissionsFromClaims should be nil for an unrestricted identity")
	}

	perms := ReadOnlyAPIPermissions("admin_service")
	perms.SubscribeDeny = []string{"secret.>"}
	agent, err := NewAgent(&UserConfig{HostID: "h1", Hostname: "host1", AccountID: accountID, Roles: []string{RoleViewer}, Permissions: perms}, kp)
	if err != nil {
		t.Fatal(err)
	}
	c := decode(t, agent.Token)
	if len(c.Permissions.Pub.Allow) != len(perms.PublishAllow) {
		t.Errorf("pub allow %v", c.Permissions.Pub.Allow)
	}
	if len(c.Permissions.Sub.Deny) != 1 || c.Permissions.Sub.Deny[0] != "secret.>" {
		t.Errorf("sub deny %v", c.Permissions.Sub.Deny)
	}
	if !c.Tags.Contains(RoleViewer) {
		t.Errorf("roles should be tags in the token: %v", c.Tags)
	}
	if got := PermissionsFromClaims(c); got == nil || len(got.PublishAllow) != len(perms.PublishAllow) {
		t.Errorf("PermissionsFromClaims = %+v", got)
	}

	u, _, err := NewUser(&UserConfig{Email: "p@test.local", AccountID: accountID, Permissions: &NATSPermissions{PublishAllow: []string{"x.>"}}}, kp)
	if err != nil {
		t.Fatal(err)
	}
	if c := decode(t, u.Token); len(c.Permissions.Pub.Allow) != 1 {
		t.Errorf("user permissions %v", c.Permissions.Pub.Allow)
	}
}

func TestReissue(t *testing.T) {
	kp := accountKey(t)
	accountID, _ := kp.PublicKey()
	agent, err := NewAgent(&UserConfig{HostID: "h1", Hostname: "host1", AccountID: accountID}, kp)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, pk := agent.Token, agent.PublicKey
	agent.Roles = []string{RoleAdministrator}
	agent.Permissions = ReadOnlyAPIPermissions("admin_service")
	if err := agent.Reissue(kp); err != nil {
		t.Fatal(err)
	}
	if agent.Token == oldToken || agent.PublicKey != pk {
		t.Error("reissue must change the token and keep the key")
	}
	c := decode(t, agent.Token)
	if c.Subject != pk || !c.Tags.Contains(RoleAdministrator) || len(c.Permissions.Pub.Allow) == 0 {
		t.Errorf("reissued claims %+v", c)
	}
	if _, err := ValidateUserToken(agent.Token, accountID); err != nil {
		t.Errorf("reissued token invalid: %v", err)
	}
	if err := (&UserBase{}).Reissue(kp); err == nil {
		t.Error("reissue without a key should fail")
	}
}

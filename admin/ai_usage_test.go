package admin

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/user"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAIUsageStore(t *testing.T) *AIUsageStore {
	t.Helper()
	us, err := NewAIUsageStore(t.TempDir()+"/ai_usage.db", 0, logger.NewLogger("unit_testing", "ai_usage"))
	require.NoError(t, err)
	t.Cleanup(func() { us.Close() })
	return us
}

func aiTestRecord(at time.Time, instance, host, account, model string, in int64, errMsg string) usage.Record {
	return usage.Record{
		ID:       ulid.MustNew(ulid.Timestamp(at), rand.Reader).String(),
		Instance: instance,
		Service:  instance,
		Host:     host,
		At:       at,
		RunRecord: ai.RunRecord{
			AccountID: account,
			Model:     model,
			Turns:     2,
			ToolCalls: 1,
			Usage:     ai.Usage{InputTokens: in, OutputTokens: 10},
			Error:     errMsg,
		},
	}
}

func TestAIUsageStore(t *testing.T) {
	us := newTestAIUsageStore(t)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	us.now = func() time.Time { return now }

	day1 := now.Add(-48 * time.Hour)
	day2 := now.Add(-24 * time.Hour)
	recs := []usage.Record{
		aiTestRecord(day1, "admin_service", "h1", "acct-a", "m1", 100, ""),
		aiTestRecord(day1.Add(time.Minute), "admin_service", "h1", "acct-a", "m2", 200, "boom"),
		aiTestRecord(day2, "agent", "h2", "acct-b", "m1", 300, ""),
		aiTestRecord(day2.Add(time.Minute), "agent", "h3", "acct-b", "m1", 400, ""),
		aiTestRecord(now.Add(-time.Minute), "admin_service", "h1", "acct-b", "m1", 500, ""),
	}
	for _, r := range recs {
		require.NoError(t, us.PutRecord(r))
	}
	// Idempotent: the same ID is stored and counted once.
	dup := recs[0]
	dup.SourceAccount = "SRC"
	require.NoError(t, us.PutRecord(dup))

	t.Run("instances from records", func(t *testing.T) {
		inst, err := us.Instances()
		require.NoError(t, err)
		require.Len(t, inst, 3)
		assert.Equal(t, []string{"admin_service@h1", "agent@h2", "agent@h3"}, []string{inst[0].ID, inst[1].ID, inst[2].ID})
		a := inst[0]
		assert.Equal(t, int64(3), a.Totals.Runs, "totals computed from records, duplicate not counted")
		assert.Equal(t, int64(1), a.Totals.Errors)
		assert.Equal(t, int64(800), a.Totals.Usage.InputTokens)
		assert.True(t, a.LastRunAt.Equal(now.Add(-time.Minute)))
		assert.True(t, a.Online, "a run a minute ago is within three default intervals")
		assert.Empty(t, a.SourceAccount, "the duplicate does not update the instance")
		assert.False(t, inst[1].Online, "silent for a day")
	})

	t.Run("instance from heartbeat", func(t *testing.T) {
		hb := usage.Heartbeat{
			Instance: "admin_service", Service: "admin_service", Host: "h1",
			Version: "v1", Provider: "anthropic", Model: "m1",
			StartedAt: now.Add(-time.Hour), At: now.Add(-2 * time.Minute),
			IntervalSeconds: 30,
			Totals:          usage.Totals{Runs: 42},
			SourceAccount:   "SYS",
		}
		require.NoError(t, us.PutHeartbeat(hb))
		inst, err := us.Instances()
		require.NoError(t, err)
		a := inst[0]
		assert.Equal(t, int64(42), a.Totals.Runs, "heartbeat totals win over records")
		assert.Equal(t, "v1", a.Version)
		assert.Equal(t, "anthropic", a.Provider)
		assert.Equal(t, 30, a.IntervalSeconds)
		assert.Equal(t, "SYS", a.SourceAccount)
		assert.True(t, a.LastSeen.Equal(now.Add(-time.Minute)), "an older heartbeat does not move last seen back")
		assert.True(t, a.LastRunAt.Equal(now.Add(-time.Minute)), "last run kept from records")
		assert.True(t, a.Online, "60 s <= 3 x 30 s")

		us.now = func() time.Time { return now.Add(40 * time.Second) }
		inst, err = us.Instances()
		require.NoError(t, err)
		assert.False(t, inst[0].Online, "100 s > 3 x 30 s")
		us.now = func() time.Time { return now }

		// A brand new instance from a heartbeat only.
		require.NoError(t, us.PutHeartbeat(usage.Heartbeat{Instance: "svc", Host: "h9", At: now, IntervalSeconds: 60}))
		inst, err = us.Instances()
		require.NoError(t, err)
		require.Len(t, inst, 4)
		assert.Equal(t, "svc@h9", inst[3].ID)
		assert.True(t, inst[3].Online)
	})

	t.Run("runs filtering and paging", func(t *testing.T) {
		all, total, err := us.Runs(AIRunQuery{})
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		require.Len(t, all, 5)
		for i := range all {
			assert.Equal(t, recs[len(recs)-1-i].ID, all[i].ID, "newest first")
		}
		page, total, err := us.Runs(AIRunQuery{Limit: 2, Offset: 1})
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		require.Len(t, page, 2)
		assert.Equal(t, recs[3].ID, page[0].ID)
		assert.Equal(t, recs[2].ID, page[1].ID)

		got, total, err := us.Runs(AIRunQuery{Instance: "agent"})
		require.NoError(t, err)
		assert.Equal(t, 2, total)
		assert.Len(t, got, 2)
		_, total, err = us.Runs(AIRunQuery{Instance: "agent", Host: "h3"})
		require.NoError(t, err)
		assert.Equal(t, 1, total)
		_, total, err = us.Runs(AIRunQuery{AccountID: "acct-b", Model: "m1"})
		require.NoError(t, err)
		assert.Equal(t, 3, total)
		got, total, err = us.Runs(AIRunQuery{Offset: 10})
		require.NoError(t, err)
		assert.Equal(t, 5, total)
		assert.Empty(t, got)
	})

	t.Run("summary", func(t *testing.T) {
		byInst, err := us.Summary(time.Time{}, AIUsageGroupInstance)
		require.NoError(t, err)
		require.Len(t, byInst, 3)
		assert.Equal(t, "admin_service@h1", byInst[0].Key)
		assert.Equal(t, int64(3), byInst[0].Totals.Runs)

		byModel, err := us.Summary(time.Time{}, AIUsageGroupModel)
		require.NoError(t, err)
		require.Len(t, byModel, 2)
		assert.Equal(t, "m1", byModel[0].Key)
		assert.Equal(t, int64(4), byModel[0].Totals.Runs)
		assert.Equal(t, int64(1300), byModel[0].Totals.Usage.InputTokens)

		byAccount, err := us.Summary(time.Time{}, AIUsageGroupAccount)
		require.NoError(t, err)
		require.Len(t, byAccount, 2)
		assert.Equal(t, AIUsageGroup{Key: "acct-b", Totals: byAccount[0].Totals}, byAccount[0])
		assert.Equal(t, int64(3), byAccount[0].Totals.Runs)

		byDay, err := us.Summary(time.Time{}, AIUsageGroupDay)
		require.NoError(t, err)
		require.Len(t, byDay, 3)
		assert.Equal(t, []string{"2026-10-16", "2026-10-17", "2026-10-18"}, []string{byDay[0].Key, byDay[1].Key, byDay[2].Key})

		recent, err := us.Summary(now.Add(-time.Hour), AIUsageGroupDay)
		require.NoError(t, err)
		require.Len(t, recent, 1)
		assert.Equal(t, int64(1), recent[0].Totals.Runs)

		_, err = us.Summary(time.Time{}, "bogus")
		assert.Error(t, err)
	})

	t.Run("prune silent instances", func(t *testing.T) {
		us.now = func() time.Time { return now.Add(AIInstancePruneAfter - 12*time.Hour) }
		defer func() { us.now = func() time.Time { return now } }()
		n, err := us.PruneInstances()
		require.NoError(t, err)
		assert.Equal(t, 2, n, "agent@h2 and agent@h3 were last seen over 30 days ago")
		inst, err := us.Instances()
		require.NoError(t, err)
		assert.Len(t, inst, 2)
	})
}

func TestAIUsageMessages(t *testing.T) {
	us := newTestAIUsageStore(t)
	r := aiTestRecord(time.Now(), "agent", "h1", "acct", "m1", 5, "")
	r.SourceAccount = "spoofed"
	data, err := json.Marshal(r)
	require.NoError(t, err)

	srv := startTestNATS(t)
	require.NoError(t, subscribeAIUsage(natsConnect(t, srv), "TENANT", us))
	pub := natsConnect(t, srv)
	require.NoError(t, pub.Publish(usage.UsageSubject("agent"), data))
	require.NoError(t, pub.Publish(usage.UsageSubject("agent"), data))
	require.NoError(t, pub.Publish(usage.UsageSubject("agent"), []byte("not json")))
	hb, err := json.Marshal(usage.Heartbeat{Instance: "agent", Host: "h1", Version: "v2", At: time.Now(), IntervalSeconds: 60})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(usage.HeartbeatSubject("agent"), hb))
	require.NoError(t, pub.Flush())

	require.Eventually(t, func() bool {
		inst, err := us.Instances()
		return err == nil && len(inst) == 1 && inst[0].Version == "v2"
	}, 5*time.Second, 20*time.Millisecond)
	runs, total, err := us.Runs(AIRunQuery{})
	require.NoError(t, err)
	require.Equal(t, 1, total, "a record published twice is stored once")
	assert.Equal(t, "TENANT", runs[0].SourceAccount, "source account comes from the connection")
	inst, err := us.Instances()
	require.NoError(t, err)
	assert.Equal(t, "TENANT", inst[0].SourceAccount)
	assert.True(t, inst[0].Online)

	assert.Error(t, us.handleAIUsageMessage("X", "sys.ai.other.agent", data))
	assert.Error(t, us.handleAIUsageMessage("X", usage.HeartbeatSubject("a"), []byte("{")))
}

func TestAIUsageHandlers(t *testing.T) {
	adminServer := newTestAdminServer(t, t.TempDir()+"/admin.db")
	t.Cleanup(adminServer.Close)
	adminAccountID := adminServer.AdminAccountID()
	tenant := makeAccount(t, adminServer, "tenant")
	adminServer.AddHandlers()
	superAdmin := makeTokenTestUser(t, adminServer, adminAccountID, "root@admin.local", []string{user.RoleAdministrator})
	tenantAdmin := makeTokenTestUser(t, adminServer, tenant, "admin@tenant.local", []string{user.RoleAdministrator})

	do := func(accountID string, u tokenTestUser, target string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		adminServer.Srv.Handler.ServeHTTP(rr, tokenRequest(t, accountID, u, http.MethodGet, target, "", nil))
		return rr
	}

	require.Nil(t, adminServer.AIUsageStore())
	rr := do(adminAccountID, superAdmin, "/api/v1/ai/usage/instances")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "ai usage store not initialized")

	require.NoError(t, adminServer.InitAIUsageStore(t.TempDir()+"/ai_usage.db", 0))
	us := adminServer.AIUsageStore()
	require.NotNil(t, us)
	now := time.Now()
	for i, model := range []string{"m1", "m1", "m2"} {
		require.NoError(t, us.PutRecord(aiTestRecord(now.Add(time.Duration(i-10)*time.Minute), "admin_service", "h1", tenant, model, 10, "")))
	}
	require.NoError(t, us.PutHeartbeat(usage.Heartbeat{Instance: "admin_service", Host: "h1", At: now, IntervalSeconds: 60, Totals: usage.Totals{Runs: 3}}))

	for _, target := range []string{"/api/v1/ai/usage/instances", "/api/v1/ai/usage/runs", "/api/v1/ai/usage/summary"} {
		rr := do(tenant, tenantAdmin, target)
		assert.Equal(t, http.StatusForbidden, rr.Code, "%s for a tenant admin", target)
	}

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/instances")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var inst []AIInstanceSummary
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &inst))
	require.Len(t, inst, 1)
	assert.Equal(t, "admin_service@h1", inst[0].ID)
	assert.True(t, inst[0].Online)
	assert.Equal(t, int64(3), inst[0].Totals.Runs)

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/runs?model=m1&limit=1")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var runs struct {
		Records []usage.Record `json:"records"`
		Total   int            `json:"total"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &runs))
	assert.Equal(t, 2, runs.Total)
	require.Len(t, runs.Records, 1)
	assert.Equal(t, "m1", runs.Records[0].Model)

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/summary?group=model")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var groups []AIUsageGroup
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &groups))
	require.Len(t, groups, 2)
	assert.Equal(t, "m1", groups[0].Key)
	assert.Equal(t, int64(2), groups[0].Totals.Runs)

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/summary")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	groups = nil
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &groups))
	require.Len(t, groups, 1)
	assert.Equal(t, "admin_service@h1", groups[0].Key)

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/summary?since="+now.Add(time.Hour).Format(time.RFC3339))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.JSONEq(t, "[]", rr.Body.String())

	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/summary?since=yesterday")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	rr = do(adminAccountID, superAdmin, "/api/v1/ai/usage/summary?group=color")
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// A record newer than the last heartbeat counts toward the instance at
// once; an older one is already in the heartbeat's totals.
func TestAIUsageStoreTotalsBetweenHeartbeats(t *testing.T) {
	st, err := NewAIUsageStore(t.TempDir(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := time.Now().UTC().Add(-time.Minute)
	hb := usage.Heartbeat{Instance: "svc", Host: "h", At: base, IntervalSeconds: 60, Totals: usage.Totals{Runs: 5}}
	if err := st.PutHeartbeat(hb); err != nil {
		t.Fatal(err)
	}
	old := usage.Record{ID: "01OLD", Instance: "svc", Host: "h", At: base.Add(-time.Second)}
	newer := usage.Record{ID: "01NEW", Instance: "svc", Host: "h", At: base.Add(time.Second)}
	newer.Turns = 2
	for _, r := range []usage.Record{old, newer, newer} {
		if err := st.PutRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	inst, err := st.Instances()
	if err != nil || len(inst) != 1 {
		t.Fatalf("instances = %v %v", inst, err)
	}
	if inst[0].Totals.Runs != 6 || inst[0].Totals.Turns != 2 {
		t.Fatalf("totals = %+v, want 6 runs (5 + the newer record once), 2 turns", inst[0].Totals)
	}
	// An out-of-order older heartbeat does not roll back.
	stale := hb
	stale.At = base.Add(-time.Hour)
	stale.Totals = usage.Totals{Runs: 1}
	if err := st.PutHeartbeat(stale); err != nil {
		t.Fatal(err)
	}
	inst, _ = st.Instances()
	if inst[0].Totals.Runs != 6 {
		t.Fatalf("stale heartbeat rolled totals back to %+v", inst[0].Totals)
	}
}

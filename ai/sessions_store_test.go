package ai

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bboxsys/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.NewStore(&store.Options{Location: t.TempDir(), Application: "test", Component: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSessionStores(t *testing.T) {
	t.Run("badger", func(t *testing.T) { testSessionStore(t, NewStoreSessions(newTestStore(t))) })
	t.Run("memory", func(t *testing.T) { testSessionStore(t, NewMemorySessions()) })
}

func testSessionStore(t *testing.T, ss SessionStore) {
	ctx := context.Background()
	first := &Session{
		AccountID: "acc",
		UserID:    "UABC",
		Title:     "first",
		Provider:  "anthropic",
		Model:     "claude-opus-5-5",
		Messages: []Message{
			{Role: RoleUser, Content: json.RawMessage(`[{"type":"text","text":"hi"}]`), Text: "hi", CreatedAt: time.Now().UTC()},
			{Role: RoleAssistant, Content: json.RawMessage(`[{"type":"text","text":"hello"}]`), Text: "hello",
				ToolCalls: []ToolCall{{ID: "t1", Name: "read", Input: json.RawMessage(`{"a":1}`)}}},
		},
		Usage: Usage{InputTokens: 3, OutputTokens: 4},
	}
	if err := ss.Put(ctx, first); err != nil {
		t.Fatalf("put: %v", err)
	}
	if first.ID == "" || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("put did not stamp the session: %+v", first)
	}

	got, err := ss.Get(ctx, "acc", "UABC", first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "first" || got.Model != first.Model || len(got.Messages) != 2 || got.Usage != first.Usage {
		t.Errorf("round trip: %+v", got)
	}
	if string(got.Messages[1].Content) != `[{"type":"text","text":"hello"}]` || string(got.Messages[1].ToolCalls[0].Input) != `{"a":1}` {
		t.Errorf("raw content lost: %s / %s", got.Messages[1].Content, got.Messages[1].ToolCalls[0].Input)
	}
	// The stored copy does not alias the caller's slices.
	got.Messages[0].Text = "changed"
	again, _ := ss.Get(ctx, "acc", "UABC", first.ID)
	if again.Messages[0].Text != "hi" {
		t.Error("Get returned shared state")
	}

	// A second session for the same owner lists first; one for another
	// owner and another account never shows.
	time.Sleep(5 * time.Millisecond)
	second := &Session{AccountID: "acc", UserID: "UABC", Title: "second"}
	other := &Session{AccountID: "acc", UserID: "UXYZ", Title: "other"}
	foreign := &Session{AccountID: "acc2", UserID: "UABC", Title: "foreign"}
	for _, s := range []*Session{second, other, foreign} {
		if err := ss.Put(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	list, err := ss.List(ctx, "acc", "UABC")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Errorf("list: %+v", list)
	}
	if list[1].MessageCount != 2 || list[1].Title != "first" {
		t.Errorf("summary: %+v", list[1])
	}
	if _, err := ss.Get(ctx, "acc", "UXYZ", first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("cross-user get: %v", err)
	}
	if _, err := ss.Get(ctx, "acc2", "UABC", first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("cross-account get: %v", err)
	}

	// Updates keep the id and move the session to the top of the list.
	time.Sleep(5 * time.Millisecond)
	first.Title = "first updated"
	if err := ss.Put(ctx, first); err != nil {
		t.Fatal(err)
	}
	list, _ = ss.List(ctx, "acc", "UABC")
	if len(list) != 2 || list[0].ID != first.ID || list[0].Title != "first updated" {
		t.Errorf("list after update: %+v", list)
	}

	if err := ss.Delete(ctx, "acc", "UXYZ", first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("cross-user delete: %v", err)
	}
	if err := ss.Delete(ctx, "acc", "UABC", first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := ss.Get(ctx, "acc", "UABC", first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := ss.Delete(ctx, "acc", "UABC", first.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("second delete: %v", err)
	}
	list, _ = ss.List(ctx, "acc", "UABC")
	if len(list) != 1 || list[0].ID != second.ID {
		t.Errorf("list after delete: %+v", list)
	}

	if err := ss.Put(ctx, &Session{AccountID: "a:b", UserID: "u"}); err == nil {
		t.Error("account id with a colon must be rejected")
	}
	if err := ss.Put(ctx, &Session{AccountID: "acc"}); err == nil {
		t.Error("empty user id must be rejected")
	}
}

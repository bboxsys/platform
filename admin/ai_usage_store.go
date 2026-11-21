package admin

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bboxsys/platform/ai/usage"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

const (
	aiUsageRecordPrefix   = "aur:"
	aiUsageInstancePrefix = "aui:"

	// DefaultAIUsageRetention is how long the admin keeps collected records.
	DefaultAIUsageRetention = 90 * 24 * time.Hour
	// AIInstancePruneAfter is how long an instance may stay silent before
	// its row is removed.
	AIInstancePruneAfter = 30 * 24 * time.Hour

	aiUsageDefaultRunsLimit = 100
	aiUsageMaxRunsLimit     = 500
)

// AI usage summary groupings.
const (
	AIUsageGroupInstance = "instance"
	AIUsageGroupModel    = "model"
	AIUsageGroupAccount  = "account"
	AIUsageGroupDay      = "day"
)

// AIInstanceSummary is one running AI instance ({instance}@{host}) as the
// collector last saw it.
type AIInstanceSummary struct {
	ID              string       `json:"id"`
	Instance        string       `json:"instance"`
	Service         string       `json:"service,omitempty"`
	Host            string       `json:"host"`
	Version         string       `json:"version,omitempty"`
	Provider        string       `json:"provider,omitempty"`
	Model           string       `json:"model,omitempty"`
	SourceAccount   string       `json:"source_account,omitempty"`
	StartedAt       time.Time    `json:"started_at"`
	LastSeen        time.Time    `json:"last_seen"`
	Online          bool         `json:"online"`
	IntervalSeconds int          `json:"interval_seconds"`
	Totals          usage.Totals `json:"totals"`
	LastRunAt       time.Time    `json:"last_run_at"`
}

// AIUsageGroup is the totals of the records sharing one grouping key.
type AIUsageGroup struct {
	Key    string       `json:"key"`
	Totals usage.Totals `json:"totals"`
}

// AIRunQuery filters and pages AIUsageStore.Runs. Empty fields match all.
type AIRunQuery struct {
	Instance  string
	Host      string
	AccountID string
	Model     string
	Limit     int
	Offset    int
}

// aiInstanceRow is the stored form of an instance.
type aiInstanceRow struct {
	AIInstanceSummary
	FirstSeen    time.Time `json:"first_seen"`
	HasHeartbeat bool      `json:"has_heartbeat"`
	// HeartbeatAt is when the instance measured the Totals of its last
	// heartbeat. A record newer than that is added to Totals as it
	// arrives, so the page is current between heartbeats; an older one is
	// already counted in them.
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

// AIUsageStore is the admin's aggregate of the AI usage every instance
// reports, on its own BadgerDB. Records are kept by ID for a retention
// period, so a record published twice is counted once; instances are kept
// by {instance}@{host} from heartbeats and records. It implements
// usage.Local for the admin's own assistant.
type AIUsageStore struct {
	*store.Store
	log       *logger.Logger
	retention time.Duration
	mu        sync.Mutex
	now       func() time.Time
}

var _ usage.Local = (*AIUsageStore)(nil)

// NewAIUsageStore opens the store at location. A zero retention means
// DefaultAIUsageRetention.
func NewAIUsageStore(location string, retention time.Duration, l *logger.Logger) (*AIUsageStore, error) {
	st, err := store.NewStore(&store.Options{
		Location:    location,
		Application: "blackbox",
		Component:   "ai_usage_store",
	})
	if err != nil {
		return nil, err
	}
	if retention <= 0 {
		retention = DefaultAIUsageRetention
	}
	return &AIUsageStore{Store: st, log: l, retention: retention, now: time.Now}, nil
}

// aiInstanceID is the key of one running instance.
func aiInstanceID(instance, host string) string { return instance + "@" + host }

func (s *AIUsageStore) exists(key string) (bool, error) {
	err := s.Store.DB.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(key))
		return err
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	return err == nil, err
}

// getInstance loads an instance row; ok is false when there is none.
func (s *AIUsageStore) getInstance(id string) (aiInstanceRow, bool, error) {
	var row aiInstanceRow
	err := s.Store.GetOne(aiUsageInstancePrefix+id, &row)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return aiInstanceRow{}, false, nil
	}
	if err != nil {
		return aiInstanceRow{}, false, err
	}
	return row, true, nil
}

// PutRecord stores a record by ID. A record whose ID is already stored is
// ignored, so retries never count twice. The instance it came from is
// created or updated.
func (s *AIUsageStore) PutRecord(r usage.Record) error {
	if r.ID == "" || r.Instance == "" {
		return errors.New("ai usage: record needs id and instance")
	}
	if r.At.IsZero() {
		r.At = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := aiUsageRecordPrefix + r.ID
	found, err := s.exists(key)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if err := s.Store.PutOneWithTTL(key, r, s.retention); err != nil {
		return err
	}

	id := aiInstanceID(r.Instance, r.Host)
	row, ok, err := s.getInstance(id)
	if err != nil {
		return err
	}
	if !ok {
		row = aiInstanceRow{
			AIInstanceSummary: AIInstanceSummary{ID: id, Instance: r.Instance, Host: r.Host},
			FirstSeen:         r.At,
		}
	}
	if r.Service != "" {
		row.Service = r.Service
	}
	if r.SourceAccount != "" {
		row.SourceAccount = r.SourceAccount
	}
	if r.At.After(row.LastRunAt) {
		row.LastRunAt = r.At
	}
	if r.At.After(row.LastSeen) {
		row.LastSeen = r.At
	}
	if row.HasHeartbeat && r.At.After(row.HeartbeatAt) {
		row.Totals.Add(r)
	}
	return s.Store.PutOne(aiUsageInstancePrefix+id, row)
}

// PutHeartbeat creates or updates the instance the heartbeat describes.
// LastSeen only moves forward (a late heartbeat never hides a newer run).
func (s *AIUsageStore) PutHeartbeat(h usage.Heartbeat) error {
	if h.Instance == "" {
		return errors.New("ai usage: heartbeat needs instance")
	}
	if h.At.IsZero() {
		h.At = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := aiInstanceID(h.Instance, h.Host)
	row, ok, err := s.getInstance(id)
	if err != nil {
		return err
	}
	if !ok {
		row = aiInstanceRow{
			AIInstanceSummary: AIInstanceSummary{ID: id, Instance: h.Instance, Host: h.Host},
			FirstSeen:         h.At,
		}
	}
	if h.Service != "" {
		row.Service = h.Service
	}
	row.Version = h.Version
	row.Provider = h.Provider
	row.Model = h.Model
	row.StartedAt = h.StartedAt
	if h.At.After(row.LastSeen) {
		row.LastSeen = h.At
	}
	row.IntervalSeconds = h.IntervalSeconds
	// A heartbeat older than the one already applied must not roll the
	// totals back.
	if !h.At.Before(row.HeartbeatAt) {
		row.Totals = h.Totals
		row.HeartbeatAt = h.At
	}
	if h.SourceAccount != "" {
		row.SourceAccount = h.SourceAccount
	}
	row.HasHeartbeat = true
	return s.Store.PutOne(aiUsageInstancePrefix+id, row)
}

func (s *AIUsageStore) instanceRows() ([]aiInstanceRow, error) {
	var rows []aiInstanceRow
	if err := s.Store.Get(aiUsageInstancePrefix, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *AIUsageStore) records() ([]usage.Record, error) {
	var recs []usage.Record
	if err := s.Store.Get(aiUsageRecordPrefix, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// Instances returns every known instance sorted by instance then host.
// An instance is online when its last sign of life is within
// usage.OfflineAfter heartbeat intervals. An instance that has not sent a
// heartbeat yet has its totals computed from the records stored here.
func (s *AIUsageStore) Instances() ([]AIInstanceSummary, error) {
	rows, err := s.instanceRows()
	if err != nil {
		return nil, err
	}
	var fromRecords map[string]*usage.Totals
	for _, row := range rows {
		if !row.HasHeartbeat {
			fromRecords = map[string]*usage.Totals{}
			break
		}
	}
	if fromRecords != nil {
		recs, err := s.records()
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			id := aiInstanceID(r.Instance, r.Host)
			t := fromRecords[id]
			if t == nil {
				t = &usage.Totals{}
				fromRecords[id] = t
			}
			t.Add(r)
		}
	}
	now := s.now()
	out := make([]AIInstanceSummary, 0, len(rows))
	for _, row := range rows {
		sum := row.AIInstanceSummary
		interval := sum.IntervalSeconds
		if interval <= 0 {
			interval = int(usage.DefaultHeartbeatInterval / time.Second)
		}
		sum.Online = !sum.LastSeen.IsZero() &&
			now.Sub(sum.LastSeen) <= time.Duration(usage.OfflineAfter*interval)*time.Second
		if !row.HasHeartbeat {
			sum.Totals = usage.Totals{}
			if t := fromRecords[sum.ID]; t != nil {
				sum.Totals = *t
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

// Runs returns the records matching q, newest first, and how many match
// in all. Limit defaults to 100 and is capped at 500.
func (s *AIUsageStore) Runs(q AIRunQuery) ([]usage.Record, int, error) {
	recs, err := s.records()
	if err != nil {
		return nil, 0, err
	}
	// Record IDs are ULIDs, so key order is time order; walk it backwards.
	matched := make([]usage.Record, 0)
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if q.Instance != "" && r.Instance != q.Instance {
			continue
		}
		if q.Host != "" && r.Host != q.Host {
			continue
		}
		if q.AccountID != "" && r.AccountID != q.AccountID {
			continue
		}
		if q.Model != "" && r.Model != q.Model {
			continue
		}
		matched = append(matched, r)
	}
	total := len(matched)
	limit := q.Limit
	if limit <= 0 {
		limit = aiUsageDefaultRunsLimit
	}
	if limit > aiUsageMaxRunsLimit {
		limit = aiUsageMaxRunsLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []usage.Record{}, total, nil
	}
	matched = matched[offset:]
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

// Summary totals the records at or after since by group: instance
// ({instance}@{host}), model, account (the run's account, else the account
// it arrived from), or day (UTC date, YYYY-MM-DD). Days sort ascending;
// the other groups by runs, most first.
func (s *AIUsageStore) Summary(since time.Time, group string) ([]AIUsageGroup, error) {
	keyOf, err := aiUsageGroupKey(group)
	if err != nil {
		return nil, err
	}
	recs, err := s.records()
	if err != nil {
		return nil, err
	}
	groups := map[string]*usage.Totals{}
	for _, r := range recs {
		if r.At.Before(since) {
			continue
		}
		k := keyOf(r)
		t := groups[k]
		if t == nil {
			t = &usage.Totals{}
			groups[k] = t
		}
		t.Add(r)
	}
	out := make([]AIUsageGroup, 0, len(groups))
	for k, t := range groups {
		out = append(out, AIUsageGroup{Key: k, Totals: *t})
	}
	sort.Slice(out, func(i, j int) bool {
		if group != AIUsageGroupDay && out[i].Totals.Runs != out[j].Totals.Runs {
			return out[i].Totals.Runs > out[j].Totals.Runs
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// errAIUsageGroup reports an unknown summary grouping.
var errAIUsageGroup = errors.New("group must be instance, model, account, or day")

func aiUsageGroupKey(group string) (func(usage.Record) string, error) {
	switch group {
	case AIUsageGroupInstance:
		return func(r usage.Record) string { return aiInstanceID(r.Instance, r.Host) }, nil
	case AIUsageGroupModel:
		return func(r usage.Record) string { return r.Model }, nil
	case AIUsageGroupAccount:
		return func(r usage.Record) string {
			if r.AccountID != "" {
				return r.AccountID
			}
			return r.SourceAccount
		}, nil
	case AIUsageGroupDay:
		return func(r usage.Record) string { return r.At.UTC().Format("2006-01-02") }, nil
	}
	return nil, errAIUsageGroup
}

// PruneInstances removes instances not seen for AIInstancePruneAfter and
// returns how many it removed.
func (s *AIUsageStore) PruneInstances() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.instanceRows()
	if err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-AIInstancePruneAfter)
	var keys [][]byte
	for _, row := range rows {
		seen := row.LastSeen
		if seen.IsZero() {
			seen = row.FirstSeen
		}
		if seen.Before(cutoff) {
			keys = append(keys, []byte(aiUsageInstancePrefix+row.ID))
		}
	}
	if len(keys) == 0 {
		return 0, nil
	}
	return len(keys), s.Store.DeleteKeys(keys)
}

// StartGC prunes silent instances and runs value-log garbage collection
// (which reclaims expired records) once a day until stop is closed.
func (s *AIUsageStore) StartGC(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if n, err := s.PruneInstances(); err != nil {
					s.log.ErrorE().Err(err).Msg("ai usage store: prune instances failed")
				} else if n > 0 {
					s.log.InfoE().Int("removed", n).Msg("ai usage store: pruned silent instances")
				}
				_ = s.Store.GarbageCollection()
			case <-stop:
				return
			}
		}
	}()
}

// handleAIUsageMessage stores one message from usage.SubjectWildcard that
// arrived in the given account: a Record on a usage subject, a Heartbeat
// on a heartbeat subject. SourceAccount is always the arrival account.
func (s *AIUsageStore) handleAIUsageMessage(accountID, subject string, data []byte) error {
	switch {
	case strings.HasPrefix(subject, usage.SubjectPrefix+".usage."):
		var r usage.Record
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		r.SourceAccount = accountID
		return s.PutRecord(r)
	case strings.HasPrefix(subject, usage.SubjectPrefix+".heartbeat."):
		var h usage.Heartbeat
		if err := json.Unmarshal(data, &h); err != nil {
			return err
		}
		h.SourceAccount = accountID
		return s.PutHeartbeat(h)
	}
	return errors.New("ai usage: unknown subject " + subject)
}

package admin

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bboxsys/platform/logentry"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

const logKeyPrefix = "log:"

// LogStore is a separate BadgerDB store for log entries with rolling buffer behavior.
type LogStore struct {
	*store.Store
	log         *logger.Logger
	maxEntries  int
	mu          sync.Mutex
	insertCount atomic.Int64
	pruneEveryN int // Trigger prune check every N inserts
}

// LogQuery defines filters for querying log entries.
type LogQuery struct {
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
	Service string `json:"service"`
	Host    string `json:"host"` // Exact match on the publishing node's hostname
	Level   string `json:"level"`
	Search  string `json:"search"`   // Substring match on message
	SortDir string `json:"sort_dir"` // "asc" or "desc" (default desc = newest first)
	// Since and Until bound the entry timestamp (inclusive); a zero time
	// leaves that end open.
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

// filtered reports whether the query narrows the entries at all.
func (q *LogQuery) filtered() bool {
	return q != nil && (q.Service != "" || q.Host != "" || q.Level != "" || q.Search != "" || !q.Since.IsZero() || !q.Until.IsZero())
}

// NewLogStore creates a new LogStore backed by BadgerDB at the given location.
func NewLogStore(location string, maxEntries int, log *logger.Logger) (*LogStore, error) {
	st, err := store.NewStore(&store.Options{
		Location:    location,
		Application: "blackbox",
		Component:   "log_store",
	})
	if err != nil {
		return nil, err
	}
	ls := &LogStore{
		Store:       st,
		log:         log,
		maxEntries:  maxEntries,
		pruneEveryN: 1000,
	}
	return ls, nil
}

// PutLogEntry stores a log entry and triggers pruning if needed.
func (ls *LogStore) PutLogEntry(entry *logentry.LogEntry) error {
	key := logKeyPrefix + entry.ID
	err := ls.Store.PutOne(key, entry)
	if err != nil {
		return err
	}

	count := ls.insertCount.Add(1)
	if count%int64(ls.pruneEveryN) == 0 {
		go func() {
			if err := ls.Prune(); err != nil {
				ls.log.ErrorE().Err(err).Msg("log store prune error")
			}
		}()
	}
	return nil
}

// GetLogEntries returns log entries matching the query with pagination.
// Returns entries, total count matching filters, and error.
func (ls *LogStore) GetLogEntries(query *LogQuery) ([]logentry.LogEntry, int, error) {
	var allEntries []logentry.LogEntry
	err := ls.Store.Get(logKeyPrefix, &allEntries)
	if err != nil {
		return nil, 0, err
	}

	// Apply filters
	filtered := ls.applyFilters(allEntries, query)
	total := len(filtered)

	// Sort: ULIDs are lexicographically time-ordered, so natural order is ascending.
	// Default is desc (newest first).
	if query.SortDir != "asc" {
		// Reverse for descending order
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	// Apply pagination
	if query.Offset > 0 {
		if query.Offset >= len(filtered) {
			return []logentry.LogEntry{}, total, nil
		}
		filtered = filtered[query.Offset:]
	}
	if query.Limit > 0 && query.Limit < len(filtered) {
		filtered = filtered[:query.Limit]
	}

	return filtered, total, nil
}

// GetLogEntriesCount returns the total count of log entries matching optional filters.
func (ls *LogStore) GetLogEntriesCount(query *LogQuery) (int, error) {
	if !query.filtered() {
		// Fast path: count all entries without deserializing
		count, err := ls.Store.GetCount(&store.Query{
			Prefix: "log",
		})
		return count, err
	}

	var allEntries []logentry.LogEntry
	err := ls.Store.Get(logKeyPrefix, &allEntries)
	if err != nil {
		return 0, err
	}
	filtered := ls.applyFilters(allEntries, query)
	return len(filtered), nil
}

// LogBucket is one group of a log summary.
type LogBucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// LogSummaryFields are the fields GetLogSummary groups by.
var LogSummaryFields = []string{"service", "host", "level", "component", "message"}

// GetLogSummary counts the entries matching query grouped by one field,
// largest group first (ties by key), at most limit groups when limit is
// positive. It is the one-call answer to "which messages are most
// common" that paging through the list cannot give cheaply.
func (ls *LogStore) GetLogSummary(query *LogQuery, by string, limit int) ([]LogBucket, error) {
	key := func(e *logentry.LogEntry) string {
		switch by {
		case "service":
			return e.Service
		case "host":
			return e.Hostname
		case "level":
			return e.Level
		case "component":
			return e.Component
		case "message":
			return e.Message
		}
		return ""
	}
	if !slices.Contains(LogSummaryFields, by) {
		return nil, fmt.Errorf("unknown field %q: use one of %s", by, strings.Join(LogSummaryFields, ", "))
	}
	var allEntries []logentry.LogEntry
	if err := ls.Store.Get(logKeyPrefix, &allEntries); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, e := range ls.applyFilters(allEntries, query) {
		counts[key(&e)]++
	}
	out := make([]LogBucket, 0, len(counts))
	for k, n := range counts {
		out = append(out, LogBucket{Key: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

// Prune deletes oldest entries when count exceeds maxEntries.
// Batch-prunes 10% of maxEntries at a time.
func (ls *LogStore) Prune() error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	count, err := ls.Store.GetCount(&store.Query{
		Prefix: "log",
	})
	if err != nil {
		return err
	}

	if count <= ls.maxEntries {
		return nil
	}

	// Delete oldest entries. Since ULIDs sort ascending by time,
	// the first entries in iteration order are the oldest.
	deleteCount := count - ls.maxEntries
	// Batch prune: delete at least 10% of max to avoid frequent pruning
	minBatch := ls.maxEntries / 10
	if minBatch < 1 {
		minBatch = 1
	}
	if deleteCount < minBatch {
		deleteCount = minBatch
	}

	var keysToDelete [][]byte
	err = ls.Store.DB.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte(logKeyPrefix)
		collected := 0
		for it.Seek(prefix); it.ValidForPrefix(prefix) && collected < deleteCount; it.Next() {
			keysToDelete = append(keysToDelete, it.Item().KeyCopy(nil))
			collected++
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(keysToDelete) > 0 {
		ls.log.InfoE().Int("pruning", len(keysToDelete)).Int("total", count).Int("maxEntries", ls.maxEntries).Msg("log store pruning")
		return ls.Store.DeleteKeys(keysToDelete)
	}
	return nil
}

// MaxEntries returns the current max entries limit.
func (ls *LogStore) MaxEntries() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.maxEntries
}

// SetMaxEntries updates the max entries limit and prunes if the new limit is lower.
func (ls *LogStore) SetMaxEntries(maxEntries int) {
	ls.mu.Lock()
	oldMax := ls.maxEntries
	ls.maxEntries = maxEntries
	ls.mu.Unlock()

	if maxEntries < oldMax {
		if err := ls.Prune(); err != nil {
			ls.log.ErrorE().Err(err).Msg("log store prune after SetMaxEntries error")
		}
	}
}

// StartGC starts periodic garbage collection on the log store.
func (ls *LogStore) StartGC(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = ls.Store.GarbageCollection()
			case <-stop:
				return
			}
		}
	}()
}

func (ls *LogStore) applyFilters(entries []logentry.LogEntry, query *LogQuery) []logentry.LogEntry {
	if !query.filtered() {
		return entries
	}

	result := make([]logentry.LogEntry, 0, len(entries))
	searchLower := strings.ToLower(query.Search)
	var sinceMS, untilMS int64
	if !query.Since.IsZero() {
		sinceMS = query.Since.UnixMilli()
	}
	if !query.Until.IsZero() {
		untilMS = query.Until.UnixMilli()
	}

	for _, e := range entries {
		if sinceMS != 0 && e.Timestamp < sinceMS {
			continue
		}
		if untilMS != 0 && e.Timestamp > untilMS {
			continue
		}
		if query.Service != "" && e.Service != query.Service {
			continue
		}
		if query.Host != "" && e.Hostname != query.Host {
			continue
		}
		if query.Level != "" && e.Level != query.Level {
			continue
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(e.Message), searchLower) {
			continue
		}
		result = append(result, e)
	}
	return result
}

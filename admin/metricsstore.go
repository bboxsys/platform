package admin

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/storemetrics"
	"github.com/bboxsys/store"
	"github.com/dgraph-io/badger/v4"
)

const metricsKeyPrefix = "sm:"

// MetricsStore is a separate BadgerDB store for store metrics entries with rolling buffer behavior.
type MetricsStore struct {
	*store.Store
	log         *logger.Logger
	maxEntries  int
	mu          sync.Mutex
	insertCount atomic.Int64
	pruneEveryN int // Trigger prune check every N inserts
}

// MetricsQuery defines filters for querying metrics entries.
type MetricsQuery struct {
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
	Service   string `json:"service"`
	StoreName string `json:"store_name"`
	SortDir   string `json:"sort_dir"` // "asc" or "desc" (default desc = newest first)
}

// NewMetricsStore creates a new MetricsStore backed by BadgerDB at the given location.
func NewMetricsStore(location string, maxEntries int, log *logger.Logger) (*MetricsStore, error) {
	st, err := store.NewStore(&store.Options{
		Location:    location,
		Application: "blackbox",
		Component:   "metrics_store",
	})
	if err != nil {
		return nil, err
	}
	ms := &MetricsStore{
		Store:       st,
		log:         log,
		maxEntries:  maxEntries,
		pruneEveryN: 1000,
	}
	return ms, nil
}

// PutMetricsEntry stores a metrics entry and triggers pruning if needed.
func (ms *MetricsStore) PutMetricsEntry(entry *storemetrics.StoreMetricsEntry) error {
	key := metricsKeyPrefix + entry.ID
	err := ms.Store.PutOne(key, entry)
	if err != nil {
		return err
	}

	count := ms.insertCount.Add(1)
	if count%int64(ms.pruneEveryN) == 0 {
		go func() {
			if err := ms.Prune(); err != nil {
				ms.log.ErrorE().Err(err).Msg("metrics store prune error")
			}
		}()
	}
	return nil
}

// GetMetricsEntries returns metrics entries matching the query with pagination.
func (ms *MetricsStore) GetMetricsEntries(query *MetricsQuery) ([]storemetrics.StoreMetricsEntry, int, error) {
	var allEntries []storemetrics.StoreMetricsEntry
	err := ms.Store.Get(metricsKeyPrefix, &allEntries)
	if err != nil {
		return nil, 0, err
	}

	// Apply filters
	filtered := ms.applyFilters(allEntries, query)
	total := len(filtered)

	// Sort: ULIDs are lexicographically time-ordered, so natural order is ascending.
	// Default is desc (newest first).
	if query.SortDir != "asc" {
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	// Apply pagination
	if query.Offset > 0 {
		if query.Offset >= len(filtered) {
			return []storemetrics.StoreMetricsEntry{}, total, nil
		}
		filtered = filtered[query.Offset:]
	}
	if query.Limit > 0 && query.Limit < len(filtered) {
		filtered = filtered[:query.Limit]
	}

	return filtered, total, nil
}

// GetMetricsEntriesCount returns the total count of metrics entries matching optional filters.
func (ms *MetricsStore) GetMetricsEntriesCount(query *MetricsQuery) (int, error) {
	if query == nil || (query.Service == "" && query.StoreName == "") {
		// Fast path: count all entries without deserializing
		count, err := ms.Store.GetCount(&store.Query{
			Prefix: "sm",
		})
		return count, err
	}

	var allEntries []storemetrics.StoreMetricsEntry
	err := ms.Store.Get(metricsKeyPrefix, &allEntries)
	if err != nil {
		return 0, err
	}
	filtered := ms.applyFilters(allEntries, query)
	return len(filtered), nil
}

// GetLatestSnapshots returns the most recent entry per service+store combination.
func (ms *MetricsStore) GetLatestSnapshots() ([]storemetrics.StoreMetricsEntry, error) {
	var allEntries []storemetrics.StoreMetricsEntry
	err := ms.Store.Get(metricsKeyPrefix, &allEntries)
	if err != nil {
		return nil, err
	}

	// Since ULIDs sort ascending by time, iterate in reverse to find latest per key.
	latest := make(map[string]storemetrics.StoreMetricsEntry)
	for i := len(allEntries) - 1; i >= 0; i-- {
		e := allEntries[i]
		key := e.Service + ":" + e.StoreName
		if _, exists := latest[key]; !exists {
			latest[key] = e
		}
	}

	result := make([]storemetrics.StoreMetricsEntry, 0, len(latest))
	for _, e := range latest {
		result = append(result, e)
	}
	return result, nil
}

// Prune deletes oldest entries when count exceeds maxEntries.
// Batch-prunes 10% of maxEntries at a time.
func (ms *MetricsStore) Prune() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	count, err := ms.Store.GetCount(&store.Query{
		Prefix: "sm",
	})
	if err != nil {
		return err
	}

	if count <= ms.maxEntries {
		return nil
	}

	// Delete oldest entries. Since ULIDs sort ascending by time,
	// the first entries in iteration order are the oldest.
	deleteCount := count - ms.maxEntries
	// Batch prune: delete at least 10% of max to avoid frequent pruning
	minBatch := ms.maxEntries / 10
	if minBatch < 1 {
		minBatch = 1
	}
	if deleteCount < minBatch {
		deleteCount = minBatch
	}

	var keysToDelete [][]byte
	err = ms.Store.DB.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte(metricsKeyPrefix)
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
		ms.log.InfoE().Int("pruning", len(keysToDelete)).Int("total", count).Int("maxEntries", ms.maxEntries).Msg("metrics store pruning")
		return ms.Store.DeleteKeys(keysToDelete)
	}
	return nil
}

// StartGC starts periodic garbage collection on the metrics store.
func (ms *MetricsStore) StartGC(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = ms.Store.GarbageCollection()
			case <-stop:
				return
			}
		}
	}()
}

func (ms *MetricsStore) applyFilters(entries []storemetrics.StoreMetricsEntry, query *MetricsQuery) []storemetrics.StoreMetricsEntry {
	if query == nil || (query.Service == "" && query.StoreName == "") {
		return entries
	}

	result := make([]storemetrics.StoreMetricsEntry, 0, len(entries))
	for _, e := range entries {
		if query.Service != "" && e.Service != query.Service {
			continue
		}
		if query.StoreName != "" && e.StoreName != query.StoreName {
			continue
		}
		result = append(result, e)
	}
	return result
}

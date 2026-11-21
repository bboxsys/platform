package storemetrics

import (
	"encoding/json"
	"time"

	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/messenger"
	"github.com/bboxsys/store"
)

// StoreInfo identifies a store to collect metrics from.
type StoreInfo struct {
	Name  string       // e.g. "admin.db"
	Store *store.Store // underlying BadgerDB store
}

// Publisher periodically collects metrics from stores and publishes them via NATS.
type Publisher struct {
	service  string
	stores   []StoreInfo
	interval time.Duration
	client   *messenger.Client
	logger   *logger.Logger
	stopChan chan struct{}
}

// NewPublisher creates a new metrics publisher.
func NewPublisher(service string, stores []StoreInfo, interval time.Duration, client *messenger.Client, l *logger.Logger) *Publisher {
	return &Publisher{
		service:  service,
		stores:   stores,
		interval: interval,
		client:   client,
		logger:   l,
		stopChan: make(chan struct{}),
	}
}

// Start begins periodic metrics collection and publishing.
func (p *Publisher) Start() {
	go p.run()
}

// Stop signals the publisher to stop.
func (p *Publisher) Stop() {
	close(p.stopChan)
}

func (p *Publisher) run() {
	// Collect immediately on start
	p.collectAndPublish()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.collectAndPublish()
		case <-p.stopChan:
			return
		}
	}
}

func (p *Publisher) collectAndPublish() {
	subject := NATSSubject(p.service)
	for _, si := range p.stores {
		metrics := si.Store.CollectMetrics()
		entry := NewStoreMetricsEntry(p.service, si.Name, metrics)
		data, err := json.Marshal(entry)
		if err != nil {
			p.logger.ErrorE().Str("service", p.service).Str("store", si.Name).Err(err).Msg("metrics publisher: error marshaling metrics")
			continue
		}
		if err := p.client.PublishRaw(subject, data); err != nil {
			p.logger.ErrorE().Str("service", p.service).Str("store", si.Name).Err(err).Msg("metrics publisher: error publishing metrics")
		}
	}
}

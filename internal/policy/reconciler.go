package policy

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ReloadFunc is a callback invoked when the reconciler detects updated policy content.
type ReloadFunc func([]byte) error

// Reconciler runs an autonomous background loop that periodically polls a PolicySource.
type Reconciler struct {
	source       *PolicySource
	fetcher      *Fetcher
	pollInterval time.Duration
	reloadFunc   ReloadFunc
	logger       *slog.Logger
	stopChan     chan struct{}
	doneChan     chan struct{}
	stopOnce     sync.Once
}

// NewReconciler constructs a Reconciler instance.
func NewReconciler(
	source *PolicySource,
	fetcher *Fetcher,
	interval time.Duration,
	reloadFunc ReloadFunc,
	logger *slog.Logger,
) *Reconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		source:       source,
		fetcher:      fetcher,
		pollInterval: interval,
		reloadFunc:   reloadFunc,
		logger:       logger,
		stopChan:     make(chan struct{}),
		doneChan:     make(chan struct{}),
	}
}

// Start launches the background reconciliation goroutine.
func (r *Reconciler) Start() {
	if r.pollInterval <= 0 {
		return
	}

	go func() {
		defer close(r.doneChan)
		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-r.stopChan:
				return
			case <-ticker.C:
				r.reconcileOnce()
			}
		}
	}()
}

func (r *Reconciler) reconcileOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var snap PolicySourceSnapshot
	if r.source != nil {
		snap = r.source.Snapshot()
	}

	data, modified, err := r.fetcher.Fetch(ctx, r.source)
	if err != nil {
		r.logger.Error("reconciliation: remote policy fetch failed", "error", err, "uri", r.source.URI)
		return
	}

	if !modified {
		// Zero allocation, no changes
		return
	}

	if r.reloadFunc != nil {
		if err := r.reloadFunc(data); err != nil {
			if r.source != nil {
				r.source.Restore(snap)
			}
			r.logger.Error("reconciliation: policy hot-swap rejected, previous configuration preserved", "error", err)
			return
		}
		r.logger.Info("reconciliation: remote policy updated and hot-swapped", "uri", r.source.URI)
	}
}

// Stop gracefully shuts down the background reconciliation loop. It is safe to call multiple times.
func (r *Reconciler) Stop() {
	r.stopOnce.Do(func() {
		if r.pollInterval > 0 {
			close(r.stopChan)
			<-r.doneChan
		}
	})
}

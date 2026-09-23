package sched

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

// ErrDraining is returned for submissions arriving after Drain has been called.
var ErrDraining = errors.New("citron is shutting down")

// ErrOverloaded is returned when a submission waits longer than the queue limit for
// a slot.
var ErrOverloaded = errors.New("citron is at capacity")

// Runner executes a submission.
type Runner interface {
	Run(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error)
}

// Scheduler bounds concurrent submissions and coordinates graceful shutdown.
// Together with the runner's per-submission testcase limit, a large submission
// cannot starve smaller ones.
type Scheduler struct {
	runner       Runner
	slots        chan struct{}
	maxQueueWait time.Duration

	wg       sync.WaitGroup
	draining atomic.Bool
	active   atomic.Int64
	queued   atomic.Int64
}

// NewScheduler returns a Scheduler running at most maxConcurrent submissions, each
// waiting at most maxQueueWait for a slot (0 means no limit).
func NewScheduler(runner Runner, maxConcurrent int, maxQueueWait time.Duration) *Scheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Scheduler{
		runner:       runner,
		slots:        make(chan struct{}, maxConcurrent),
		maxQueueWait: maxQueueWait,
	}
}

// Submit runs sub, waiting for a slot if necessary.
func (s *Scheduler) Submit(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}

	// Bounded so an overloaded server refuses quickly instead of letting clients time out.
	waitCtx := ctx
	if s.maxQueueWait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, s.maxQueueWait)
		defer cancel()
	}

	s.queued.Add(1)
	select {
	case s.slots <- struct{}{}:
		s.queued.Add(-1)
	case <-waitCtx.Done():
		s.queued.Add(-1)
		if ctx.Err() == nil {
			// The queue limit expired, not the caller's context.
			return judge.SubmissionResult{}, ErrOverloaded
		}
		return judge.SubmissionResult{}, ctx.Err()
	}
	defer func() { <-s.slots }()

	// Re-check: Drain may have started while queued, and wg must not grow after it.
	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}

	s.wg.Add(1)
	defer s.wg.Done()
	s.active.Add(1)
	defer s.active.Add(-1)

	return s.runner.Run(ctx, sub)
}

// Drain stops accepting submissions and waits for running ones to finish. It returns
// ctx's error if ctx expires first; the caller should then cancel running work.
func (s *Scheduler) Drain(ctx context.Context) error {
	s.draining.Store(true)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Active returns the number of running submissions.
func (s *Scheduler) Active() int64 { return s.active.Load() }

// Queued returns the number of submissions waiting for a slot.
func (s *Scheduler) Queued() int64 { return s.queued.Load() }

// Draining reports whether Drain has been called.
func (s *Scheduler) Draining() bool { return s.draining.Load() }

package sched

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JustModo/judge/internal/judge"
)

// ErrDraining is returned once shutdown has begun. It is a refusal to start new work,
// not a failure of the work already running.
var ErrDraining = errors.New("judge is shutting down")

// ErrOverloaded is returned when the judge is already running as much as it is
// configured to. Degrading into a clear refusal beats accepting work that will then
// time out on the client.
var ErrOverloaded = errors.New("judge is at capacity")

// Runner is the piece that actually executes a submission.
type Runner interface {
	Run(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error)
}

// Scheduler bounds how many submissions run at once and makes shutdown orderly.
//
// Fairness comes from two limits working together: a cap on concurrent submissions
// here, and a cap on concurrent testcases per submission inside the runner. A
// thousand-testcase submission therefore occupies one submission slot and a handful
// of executions, leaving room for the ten-testcase submissions behind it.
// ponytail: FIFO plus per-submission caps. Add weighted round-robin only if queue
// wait for small submissions actually regresses behind a large neighbour.
type Scheduler struct {
	runner       Runner
	slots        chan struct{}
	maxQueueWait time.Duration

	wg       sync.WaitGroup
	draining atomic.Bool
	active   atomic.Int64
	queued   atomic.Int64
}

// NewScheduler bounds concurrent submissions, and bounds how long one may wait for a
// slot. The wait matters as much as the limit: without it an overloaded judge keeps
// accepting work and every client eventually times out having been told nothing,
// which is worse than being refused immediately.
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

// Submit runs a submission, waiting for a slot if necessary.
func (s *Scheduler) Submit(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}

	// Bounded wait. Past this the judge is not going to get to this submission in
	// time to be useful, so it says so rather than holding the connection until the
	// client gives up.
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
			// The caller is still waiting; it was our own queue limit that expired.
			return judge.SubmissionResult{}, ErrOverloaded
		}
		return judge.SubmissionResult{}, ctx.Err()
	}
	defer func() { <-s.slots }()

	// Checked again after waiting: draining may have started while queued, and the
	// WaitGroup must not be incremented once Drain is counting down.
	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}

	s.wg.Add(1)
	defer s.wg.Done()
	s.active.Add(1)
	defer s.active.Add(-1)

	return s.runner.Run(ctx, sub)
}

// TrySubmit refuses rather than waits when every slot is busy. The HTTP layer uses it
// to answer 503 instead of holding a connection open past the client's patience.
func (s *Scheduler) TrySubmit(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return judge.SubmissionResult{}, ErrOverloaded
	}
	defer func() { <-s.slots }()

	if s.draining.Load() {
		return judge.SubmissionResult{}, ErrDraining
	}

	s.wg.Add(1)
	defer s.wg.Done()
	s.active.Add(1)
	defer s.active.Add(-1)

	return s.runner.Run(ctx, sub)
}

// Drain stops accepting submissions and waits for the running ones to finish. If ctx
// expires first it returns the context's error; the caller then cancels the root
// context, which kills the sandboxes and their process trees.
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

func (s *Scheduler) Active() int64  { return s.active.Load() }
func (s *Scheduler) Queued() int64  { return s.queued.Load() }
func (s *Scheduler) Draining() bool { return s.draining.Load() }
func (s *Scheduler) Capacity() int  { return cap(s.slots) }

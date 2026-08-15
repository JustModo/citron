package sched

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustModo/citron/internal/judge"
)

func TestAdmissionBoundsConcurrentMemory(t *testing.T) {
	a := NewAdmitter(512, 10) // 512 MB, plenty of slots

	r1, err := a.Acquire(context.Background(), 256<<20)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.Acquire(context.Background(), 256<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.ReservedMB(); got != 512 {
		t.Errorf("reserved %d MB, want 512", got)
	}

	// The budget is spent; a third execution must wait rather than over-commit.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Acquire(ctx, 256<<20); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("third acquire returned %v; it should have blocked on the budget", err)
	}

	r1()
	if _, err := a.Acquire(context.Background(), 256<<20); err != nil {
		t.Errorf("releasing capacity did not admit the waiter: %v", err)
	}
	r2()
}

func TestExecutionSlotsBoundConcurrency(t *testing.T) {
	a := NewAdmitter(10_000, 2) // memory is not the constraint here

	r1, _ := a.Acquire(context.Background(), 1<<20)
	r2, _ := a.Acquire(context.Background(), 1<<20)
	defer r1()
	defer r2()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.Acquire(ctx, 1<<20); err == nil {
		t.Error("a third execution ran with only two slots configured")
	}
}

// An execution larger than the whole budget can never be admitted. It must fail
// immediately instead of blocking forever.
func TestOversizedExecutionIsRejectedNotDeadlocked(t *testing.T) {
	a := NewAdmitter(256, 2)

	done := make(chan error, 1)
	go func() {
		_, err := a.Acquire(context.Background(), 512<<20)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("got %v, want ErrTooLarge", err)
		}
	case <-time.After(time.Second):
		t.Fatal("acquire blocked forever on a request that can never be satisfied")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	a := NewAdmitter(256, 2)
	release, err := a.Acquire(context.Background(), 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if got := a.ReservedMB(); got != 0 {
		t.Errorf("double release corrupted the budget: %d MB still reserved", got)
	}
	if got := a.InFlight(); got != 0 {
		t.Errorf("in-flight count is %d after release", got)
	}
}

func TestAdmissionIsRaceFree(t *testing.T) {
	a := NewAdmitter(1024, 8)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := a.Acquire(context.Background(), 64<<20)
			if err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			release()
		}()
	}
	wg.Wait()
	if got := a.ReservedMB(); got != 0 {
		t.Errorf("%d MB leaked after all executions finished", got)
	}
}

// --- scheduler ---

type fakeRunner struct {
	started  atomic.Int64
	peak     atomic.Int64
	inFlight atomic.Int64
	delay    time.Duration
	block    chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, sub judge.Submission) (judge.SubmissionResult, error) {
	f.started.Add(1)
	n := f.inFlight.Add(1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return judge.SubmissionResult{}, ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return judge.SubmissionResult{}, ctx.Err()
		}
	}
	return judge.SubmissionResult{ID: sub.ID, Status: judge.StatusAccepted}, nil
}

func testSubmission(id string) judge.Submission {
	return judge.Submission{ID: judge.SubmissionID(id)}
}

func TestSchedulerBoundsConcurrentSubmissions(t *testing.T) {
	r := &fakeRunner{delay: 50 * time.Millisecond}
	s := NewScheduler(r, 2, 0)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Submit(context.Background(), testSubmission(string(rune('a'+i)))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if got := r.started.Load(); got != 10 {
		t.Errorf("ran %d submissions, want 10", got)
	}
	if got := r.peak.Load(); got > 2 {
		t.Errorf("%d submissions ran at once; the limit was 2", got)
	}
}

func TestDrainWaitsForRunningWorkAndRefusesNew(t *testing.T) {
	block := make(chan struct{})
	r := &fakeRunner{block: block}
	s := NewScheduler(r, 4, 0)

	finished := make(chan error, 1)
	go func() {
		_, err := s.Submit(context.Background(), testSubmission("in-flight"))
		finished <- err
	}()

	// Wait for it to actually be running before draining.
	for s.Active() == 0 {
		time.Sleep(time.Millisecond)
	}

	drained := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		drained <- s.Drain(ctx)
	}()

	// New work is refused the moment draining starts.
	time.Sleep(20 * time.Millisecond)
	if _, err := s.Submit(context.Background(), testSubmission("late")); !errors.Is(err, ErrDraining) {
		t.Errorf("submission during drain returned %v, want ErrDraining", err)
	}

	select {
	case <-drained:
		t.Fatal("drain returned while a submission was still running")
	default:
	}

	close(block)
	if err := <-finished; err != nil {
		t.Errorf("in-flight submission failed: %v", err)
	}
	if err := <-drained; err != nil {
		t.Errorf("drain: %v", err)
	}
}

func TestDrainTimesOutRatherThanHanging(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})} // never released
	s := NewScheduler(r, 2, 0)

	go s.Submit(context.Background(), testSubmission("stuck"))
	for s.Active() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("drain returned %v; it must give up so shutdown can force the kill", err)
	}
}

func TestTrySubmitRefusesInsteadOfQueuing(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	r := &fakeRunner{block: block}
	s := NewScheduler(r, 1, 0)

	go s.TrySubmit(context.Background(), testSubmission("first"))
	for s.Active() == 0 {
		time.Sleep(time.Millisecond)
	}

	if _, err := s.TrySubmit(context.Background(), testSubmission("second")); !errors.Is(err, ErrOverloaded) {
		t.Errorf("got %v, want ErrOverloaded", err)
	}
}

// A large submission must not stop small ones from being served.
func TestSmallSubmissionsAreNotStarvedByALargeOne(t *testing.T) {
	r := &fakeRunner{delay: 10 * time.Millisecond}
	s := NewScheduler(r, 2, 0)

	big := make(chan struct{})
	go func() {
		defer close(big)
		// Stands in for a submission with very many testcases.
		slow := &fakeRunner{delay: 500 * time.Millisecond}
		NewScheduler(slow, 1, 0).Submit(context.Background(), testSubmission("big"))
	}()

	start := time.Now()
	for i := range 5 {
		if _, err := s.Submit(context.Background(), testSubmission(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	if elapsed > 400*time.Millisecond {
		t.Errorf("small submissions took %v while a large one was running", elapsed)
	}
	<-big
}

// Under sustained overload citron must refuse work rather than queue it forever.
// Without this, every client waits out its own timeout having been told nothing —
// which looks identical to citron being down.
func TestQueueWaitShedsLoadInsteadOfHangingClients(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	r := &fakeRunner{block: block}
	s := NewScheduler(r, 1, 50*time.Millisecond)

	go s.Submit(context.Background(), testSubmission("occupier"))
	for s.Active() == 0 {
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	_, err := s.Submit(context.Background(), testSubmission("queued"))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrOverloaded) {
		t.Errorf("got %v, want ErrOverloaded", err)
	}
	if elapsed > time.Second {
		t.Errorf("waited %v; the queue bound was 50ms", elapsed)
	}
}

// A caller that gives up first should see its own cancellation, not a misleading
// "overloaded" that blames citron.
func TestClientCancellationIsNotReportedAsOverload(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	r := &fakeRunner{block: block}
	s := NewScheduler(r, 1, time.Minute)

	go s.Submit(context.Background(), testSubmission("occupier"))
	for s.Active() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.Submit(ctx, testSubmission("impatient")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the caller's own deadline error", err)
	}
}

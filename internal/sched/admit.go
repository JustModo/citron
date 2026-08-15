// Package sched decides what is allowed to run and when.
//
// Two separate limits, because they run out for different reasons: memory, which the
// machine physically has a fixed amount of, and execution slots, which stand in for
// CPU. Reserving before starting is what keeps the judge off the OOM killer — a
// process the kernel chooses to kill is not necessarily the one that misbehaved.
package sched

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/semaphore"

	"github.com/JustModo/judge/internal/judge"
)

// ErrTooLarge means the execution could never be admitted, whatever the machine is
// doing. Blocking on it would be a deadlock, so it fails immediately.
var ErrTooLarge = errors.New("execution exceeds the total memory budget")

type Admitter struct {
	memory   *semaphore.Weighted
	slots    *semaphore.Weighted
	budgetMB int64

	held     atomic.Int64 // MB currently reserved, for metrics
	inFlight atomic.Int64
}

func NewAdmitter(budgetMB int64, slots int) *Admitter {
	if slots <= 0 {
		slots = 1
	}
	if budgetMB <= 0 {
		budgetMB = 1
	}
	return &Admitter{
		memory:   semaphore.NewWeighted(budgetMB),
		slots:    semaphore.NewWeighted(int64(slots)),
		budgetMB: budgetMB,
	}
}

// Acquire reserves capacity for one execution and returns the function that gives it
// back. The returned function is safe to call once; callers should defer it.
func (a *Admitter) Acquire(ctx context.Context, mem judge.MemoryBytes) (func(), error) {
	want := mem.MB()
	if want <= 0 {
		want = 1
	}
	if want > a.budgetMB {
		return nil, fmt.Errorf("%w: needs %d MB, budget is %d MB", ErrTooLarge, want, a.budgetMB)
	}

	// Memory first: it is the scarcer resource, and holding a slot while waiting for
	// memory would let a large execution block small ones that could have run.
	if err := a.memory.Acquire(ctx, want); err != nil {
		return nil, err
	}
	if err := a.slots.Acquire(ctx, 1); err != nil {
		a.memory.Release(want)
		return nil, err
	}

	a.held.Add(want)
	a.inFlight.Add(1)

	var once atomic.Bool
	return func() {
		if !once.CompareAndSwap(false, true) {
			return
		}
		a.slots.Release(1)
		a.memory.Release(want)
		a.held.Add(-want)
		a.inFlight.Add(-1)
	}, nil
}

// ReservedMB is how much of the budget is currently held.
func (a *Admitter) ReservedMB() int64 { return a.held.Load() }

// InFlight is how many executions are running.
func (a *Admitter) InFlight() int64 { return a.inFlight.Load() }

// BudgetMB is the total memory the judge is allowed to commit.
func (a *Admitter) BudgetMB() int64 { return a.budgetMB }

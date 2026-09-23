package sched

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/semaphore"

	"github.com/JustModo/citron/internal/judge"
)

// ErrTooLarge is returned immediately when a request exceeds the total memory
// budget, since waiting for it would block forever.
var ErrTooLarge = errors.New("execution exceeds the total memory budget")

// Admitter reserves memory and execution slots from fixed budgets.
type Admitter struct {
	memory   *semaphore.Weighted
	slots    *semaphore.Weighted
	budgetMB int64

	held     atomic.Int64 // MB currently reserved
	inFlight atomic.Int64
}

// NewAdmitter returns an Admitter with budgetMB of memory and the given number of
// slots; non-positive values are treated as 1.
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

// Acquire blocks until capacity for one execution is available and returns a release
// function. Release is idempotent.
func (a *Admitter) Acquire(ctx context.Context, mem judge.MemoryBytes) (func(), error) {
	want := mem.MB()
	if want <= 0 {
		want = 1
	}
	if want > a.budgetMB {
		return nil, fmt.Errorf("%w: needs %d MB, budget is %d MB", ErrTooLarge, want, a.budgetMB)
	}

	// Memory first: holding a slot while waiting for memory would let a large
	// execution block small ones that fit.
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

// ReservedMB returns the memory currently reserved, in MB.
func (a *Admitter) ReservedMB() int64 { return a.held.Load() }

// InFlight returns the number of executions holding capacity.
func (a *Admitter) InFlight() int64 { return a.inFlight.Load() }

// BudgetMB returns the total memory budget, in MB.
func (a *Admitter) BudgetMB() int64 { return a.budgetMB }

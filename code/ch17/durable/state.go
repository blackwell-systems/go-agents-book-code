// Package durable keeps an agent's run on disk: a checkpoint file per
// run, written so that a crash leaves a whole one, a lock so that one
// process at a time runs it, and a count of restarts (chapter 15).
package durable

import (
	"errors"
	"fmt"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
)

// Errors callers check for.
var (
	// ErrNoCheckpoint means the run has never been saved: start it.
	ErrNoCheckpoint = errors.New("durable: no checkpoint")

	// ErrLocked means another process has the run open.
	ErrLocked = errors.New("durable: run is open in another process")

	// ErrVersion means the checkpoint was written by newer code.
	ErrVersion = errors.New("durable: checkpoint version not supported")

	// ErrMaxResumes means the run was restarted as many times as the
	// caller allows, so it stops trying: a run that crashes every time
	// would otherwise cost money forever. It is in the ErrBudget
	// family.
	ErrMaxResumes = fmt.Errorf("%w: resume limit reached",
		agent.ErrBudget)
)

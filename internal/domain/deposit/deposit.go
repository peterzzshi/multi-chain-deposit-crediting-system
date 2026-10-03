// Package deposit implements the shared deposit lifecycle state machine
// (ADR 0005). The machine is pure: Transition decides the next state and
// the required ledger effect as data. Persisting the state and executing
// the effect atomically is the crediting engine's job (ADR 0003).
package deposit

import (
	"errors"
	"fmt"
)

// State is a deposit lifecycle state. The zero value is the empty state
// of a transfer that has not been observed yet. Match on the constants
// directly; terminal states are StateFinalized, StateDropped,
// StateReversed (for its credit cycle — EventReincluded reopens it), and
// StateBelowMinimum. See docs/state-machine.md for the canonical diagram.
type State string

const (
	StateNone         State = ""
	StatePending      State = "PENDING"
	StateCredited     State = "CREDITED"
	StateFinalized    State = "FINALIZED"
	StateReorged      State = "REORGED"
	StateDropped      State = "DROPPED"
	StateReversed     State = "REVERSED"
	StateBelowMinimum State = "BELOW_MINIMUM"
)

// Event is a fact, observed by an ingest source, that can drive a
// transition. Events carry everything the machine needs; it reads nothing
// from the environment.
type Event string

const (
	// EventObserved reports a supported-asset transfer at or above the
	// configured minimum, observed on a candidate chain.
	EventObserved Event = "OBSERVED"
	// EventObservedBelowMinimum reports a supported-asset transfer under
	// the configured minimum.
	EventObservedBelowMinimum Event = "OBSERVED_BELOW_MINIMUM"
	// EventDepthReached reports confirmation depth N_credit on the
	// canonical chain.
	EventDepthReached Event = "DEPTH_REACHED"
	// EventFinalityReached reports the finality horizon N_finalize.
	EventFinalityReached Event = "FINALITY_REACHED"
	// EventReorgedOut reports the transfer's inclusion is no longer
	// canonical.
	EventReorgedOut Event = "REORGED_OUT"
	// EventReincluded reports the transfer is back on the canonical chain.
	EventReincluded Event = "REINCLUDED"
	// EventWindowExpiredUncredited reports the reorg window expired for a
	// transfer that was never credited.
	EventWindowExpiredUncredited Event = "WINDOW_EXPIRED_UNCREDITED"
	// EventWindowExpiredCredited reports the reorg window expired for a
	// transfer that was credited; a reversal is due.
	EventWindowExpiredCredited Event = "WINDOW_EXPIRED_CREDITED"
)

// Effect is the ledger action a transition requires, returned as data.
// The crediting engine executes it in the same database transaction as
// the state change (ADR 0003); the machine itself performs no I/O. The
// zero value means no action.
type Effect string

const (
	EffectNone    Effect = ""
	EffectCredit  Effect = "CREDIT"
	EffectReverse Effect = "REVERSE"
)

// transition is one table row: the next state and its ledger effect.
type transition struct {
	next   State
	effect Effect
}

// table is the complete transition function. Any (state, event) pair not
// listed is illegal.
var table = map[State]map[Event]transition{
	StateNone: {
		EventObserved:             {StatePending, EffectNone},
		EventObservedBelowMinimum: {StateBelowMinimum, EffectNone},
	},
	StatePending: {
		EventDepthReached: {StateCredited, EffectCredit},
		EventReorgedOut:   {StateReorged, EffectNone},
	},
	StateCredited: {
		EventFinalityReached: {StateFinalized, EffectNone},
		EventReorgedOut:      {StateReorged, EffectNone},
	},
	StateReorged: {
		EventReincluded:              {StatePending, EffectNone},
		EventWindowExpiredUncredited: {StateDropped, EffectNone},
		EventWindowExpiredCredited:   {StateReversed, EffectReverse},
	},
	StateReversed: {
		// Re-inclusion opens a new credit cycle at PENDING; the old cycle
		// stays reversed.
		EventReincluded: {StatePending, EffectNone},
	},
}

// ErrIllegalTransition categorizes events that do not apply to a state.
var ErrIllegalTransition = errors.New("deposit: illegal transition")

// Transition applies an event to a state and returns the next state and
// the ledger effect to execute with it. The function is total: any pair
// not in the table fails with ErrIllegalTransition.
func Transition(s State, e Event) (State, Effect, error) {
	t, ok := table[s][e]
	if !ok {
		return StateNone, EffectNone, fmt.Errorf("%w: %s + %s", ErrIllegalTransition, s, e)
	}
	return t.next, t.effect, nil
}

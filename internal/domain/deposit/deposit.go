// Package deposit implements the shared deposit lifecycle state machine
// (ADR 0005) as a pure function: Transition returns the next state and the
// ledger effect as data; executing both atomically is the engine's job.
package deposit

import (
	"fmt"

	"deposit-crediting/internal/errs"
)

// State is a deposit lifecycle state; the zero value is a transfer not yet
// observed. See docs/state-machine.md for the canonical diagram.
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

// Event is an observed fact that can drive a transition.
type Event string

const (
	EventObserved                Event = "OBSERVED"
	EventObservedBelowMinimum    Event = "OBSERVED_BELOW_MINIMUM"
	EventDepthReached            Event = "DEPTH_REACHED"             // confirmation depth N_credit
	EventFinalityReached         Event = "FINALITY_REACHED"          // finality horizon N_finalize
	EventReorgedOut              Event = "REORGED_OUT"               // inclusion no longer canonical
	EventReincluded              Event = "REINCLUDED"                // back on the canonical chain
	EventWindowExpiredUncredited Event = "WINDOW_EXPIRED_UNCREDITED" // reorg window over, never credited
	EventWindowExpiredCredited   Event = "WINDOW_EXPIRED_CREDITED"   // reorg window over, reversal due
)

// Effect is the ledger action the engine must execute with the state change
// (ADR 0003); the zero value means no action.
type Effect string

const (
	EffectNone    Effect = ""
	EffectCredit  Effect = "CREDIT"
	EffectReverse Effect = "REVERSE"
)

type transition struct {
	next   State
	effect Effect
}

// table is the complete transition function, including redeliveries: an
// event already reflected in the state is an identity transition, not an
// error. Pairs not listed fail with errs.ErrIllegalTransition.
var table = map[State]map[Event]transition{
	StateNone: {
		EventObserved:             {StatePending, EffectNone},
		EventObservedBelowMinimum: {StateBelowMinimum, EffectNone},
	},
	StatePending: {
		EventDepthReached: {StateCredited, EffectCredit},
		EventReorgedOut:   {StateReorged, EffectNone},
		EventReincluded:   {StatePending, EffectNone}, // redelivery
	},
	StateCredited: {
		EventFinalityReached: {StateFinalized, EffectNone},
		EventReorgedOut:      {StateReorged, EffectNone},
		EventDepthReached:    {StateCredited, EffectNone}, // redelivery
	},
	StateFinalized: {
		EventDepthReached:    {StateFinalized, EffectNone}, // late duplicate
		EventFinalityReached: {StateFinalized, EffectNone}, // redelivery
	},
	StateReorged: {
		EventReincluded:              {StatePending, EffectNone},
		EventWindowExpiredUncredited: {StateDropped, EffectNone},
		EventWindowExpiredCredited:   {StateReversed, EffectReverse},
		EventReorgedOut:              {StateReorged, EffectNone}, // redelivery
	},
	StateDropped: {
		EventReorgedOut:              {StateDropped, EffectNone}, // late duplicate
		EventWindowExpiredUncredited: {StateDropped, EffectNone}, // redelivery
	},
	StateReversed: {
		// Re-inclusion opens a new credit cycle at PENDING; the old cycle
		// stays reversed.
		EventReincluded:            {StatePending, EffectNone},
		EventReorgedOut:            {StateReversed, EffectNone}, // late duplicate
		EventWindowExpiredCredited: {StateReversed, EffectNone}, // redelivery
	},
}

// Transition applies an event to a state and returns the next state and the
// ledger effect to execute with it.
func Transition(s State, e Event) (State, Effect, error) {
	t, ok := table[s][e]
	if !ok {
		return StateNone, EffectNone, fmt.Errorf("%w: %s + %s", errs.ErrIllegalTransition, s, e)
	}
	return t.next, t.effect, nil
}

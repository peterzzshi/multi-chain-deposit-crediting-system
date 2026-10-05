package domain

import (
	"fmt"

	"deposit-crediting/internal/errs"
)

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

// Transition applies an event to a state and returns the next state and the
// ledger effect to execute with it. Pure function: same input always produces
// the same output.
func Transition(s State, e Event) (State, Effect, error) {
	switch s {
	case StateNone:
		switch e {
		case EventObserved:
			return StatePending, EffectNone, nil
		case EventObservedBelowMinimum:
			return StateBelowMinimum, EffectNone, nil
		}
	case StatePending:
		switch e {
		case EventDepthReached:
			return StateCredited, EffectCredit, nil
		case EventReorgedOut:
			return StateReorged, EffectNone, nil
		case EventReincluded: // redelivery
			return StatePending, EffectNone, nil
		}
	case StateCredited:
		switch e {
		case EventFinalityReached:
			return StateFinalized, EffectNone, nil
		case EventReorgedOut:
			return StateReorged, EffectNone, nil
		case EventDepthReached: // redelivery
			return StateCredited, EffectNone, nil
		}
	case StateFinalized:
		switch e {
		case EventDepthReached, EventFinalityReached: // late duplicate, redelivery
			return StateFinalized, EffectNone, nil
		}
	case StateReorged:
		switch e {
		case EventReincluded:
			return StatePending, EffectNone, nil
		case EventWindowExpiredUncredited:
			return StateDropped, EffectNone, nil
		case EventWindowExpiredCredited:
			return StateReversed, EffectReverse, nil
		case EventReorgedOut: // redelivery
			return StateReorged, EffectNone, nil
		}
	case StateDropped:
		switch e {
		case EventReorgedOut, EventWindowExpiredUncredited: // late duplicate, redelivery
			return StateDropped, EffectNone, nil
		}
	case StateReversed:
		switch e {
		case EventReincluded: // re-inclusion starts a new credit cycle
			return StatePending, EffectNone, nil
		case EventReorgedOut, EventWindowExpiredCredited: // late duplicate, redelivery
			return StateReversed, EffectNone, nil
		}
	}
	return StateNone, EffectNone, fmt.Errorf("%w: %s + %s", errs.ErrIllegalTransition, s, e)
}

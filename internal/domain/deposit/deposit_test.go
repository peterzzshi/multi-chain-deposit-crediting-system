package deposit

import (
	"errors"
	"testing"
)

// TestTransitionTable covers every legal (state, event) pair in
// docs/state-machine.md.
func TestTransitionTable(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		event      Event
		wantState  State
		wantEffect Effect
	}{
		{"observed opens pending", StateNone, EventObserved, StatePending, EffectNone},
		{"below minimum recorded terminally", StateNone, EventObservedBelowMinimum, StateBelowMinimum, EffectNone},
		{"depth reached credits", StatePending, EventDepthReached, StateCredited, EffectCredit},
		{"finality horizon finalizes", StateCredited, EventFinalityReached, StateFinalized, EffectNone},
		{"pending reorged out", StatePending, EventReorgedOut, StateReorged, EffectNone},
		{"credited reorged out", StateCredited, EventReorgedOut, StateReorged, EffectNone},
		{"re-inclusion returns to pending", StateReorged, EventReincluded, StatePending, EffectNone},
		{"uncredited expiry drops", StateReorged, EventWindowExpiredUncredited, StateDropped, EffectNone},
		{"credited expiry reverses", StateReorged, EventWindowExpiredCredited, StateReversed, EffectReverse},
		{"re-inclusion after reversal opens new cycle", StateReversed, EventReincluded, StatePending, EffectNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotEffect, err := Transition(tt.state, tt.event)
			if err != nil {
				t.Fatalf("Transition(%s, %s) unexpected error: %v", tt.state, tt.event, err)
			}
			if gotState != tt.wantState || gotEffect != tt.wantEffect {
				t.Errorf("Transition(%s, %s) = (%s, %v); want (%s, %v)",
					tt.state, tt.event, gotState, gotEffect, tt.wantState, tt.wantEffect)
			}
		})
	}
}

// TestTransitionRejectsIllegalPairs pins down pairs that must never
// happen, so a buggy caller fails loudly instead of corrupting state.
func TestTransitionRejectsIllegalPairs(t *testing.T) {
	tests := []struct {
		name  string
		state State
		event Event
	}{
		{"finalized cannot reorg out", StateFinalized, EventReorgedOut},
		{"finalized cannot credit again", StateFinalized, EventDepthReached},
		{"dropped is absorbing", StateDropped, EventReincluded},
		{"below minimum is absorbing", StateBelowMinimum, EventDepthReached},
		{"pending cannot finalize", StatePending, EventFinalityReached},
		{"credited cannot be re-observed", StateCredited, EventObserved},
		{"reorged cannot credit directly", StateReorged, EventDepthReached},
		{"reversed cannot reverse again", StateReversed, EventWindowExpiredCredited},
		{"entry cannot skip observation", StateNone, EventDepthReached},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Transition(tt.state, tt.event)
			if !errors.Is(err, ErrIllegalTransition) {
				t.Errorf("Transition(%s, %s) error = %v; want ErrIllegalTransition", tt.state, tt.event, err)
			}
		})
	}
}

// TestFullLifecycle walks the happy path and the reorg-reversal path end
// to end, mirroring the walkthrough scenarios.
func TestFullLifecycle(t *testing.T) {
	t.Run("observe credit finalize", func(t *testing.T) {
		s := StateNone
		for _, e := range []Event{EventObserved, EventDepthReached, EventFinalityReached} {
			next, _, err := Transition(s, e)
			if err != nil {
				t.Fatalf("Transition(%s, %s) unexpected error: %v", s, e, err)
			}
			s = next
		}
		if got, want := s, StateFinalized; got != want {
			t.Errorf("final state = %s; want %s", got, want)
		}
	})

	t.Run("credit then deep reorg reverses", func(t *testing.T) {
		s := StateNone
		var reversed Effect
		for _, e := range []Event{EventObserved, EventDepthReached, EventReorgedOut, EventWindowExpiredCredited} {
			next, eff, err := Transition(s, e)
			if err != nil {
				t.Fatalf("Transition(%s, %s) unexpected error: %v", s, e, err)
			}
			s, reversed = next, eff
		}
		if got, want := s, StateReversed; got != want {
			t.Errorf("final state = %s; want %s", got, want)
		}
		if got, want := reversed, EffectReverse; got != want {
			t.Errorf("final effect = %v; want %v", got, want)
		}
	})
}

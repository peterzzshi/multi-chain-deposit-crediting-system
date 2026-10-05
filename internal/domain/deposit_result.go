package domain

// OpenResult describes the outcome of attempting to open a domain.
type OpenResult struct {
	Outcome OpenOutcome
	State   State
}

// OpenOutcome classifies what happened during deposit creation.
type OpenOutcome int

const (
	// OutcomeCreated means the deposit was inserted successfully.
	OutcomeCreated OpenOutcome = iota
	// OutcomeAlreadyExists means a deposit with this ID already exists.
	OutcomeAlreadyExists
)

// Created returns true if the deposit was newly created.
func (r OpenResult) Created() bool {
	return r.Outcome == OutcomeCreated
}

// ExistingState returns the state if the deposit already existed, or StateNone.
func (r OpenResult) ExistingState() State {
	if r.Outcome == OutcomeAlreadyExists {
		return r.State
	}
	return StateNone
}

// NewCreated returns a result indicating successful creation.
func NewCreated() OpenResult {
	return OpenResult{Outcome: OutcomeCreated, State: StateNone}
}

// NewAlreadyExists returns a result indicating the deposit already exists.
func NewAlreadyExists(state State) OpenResult {
	return OpenResult{Outcome: OutcomeAlreadyExists, State: state}
}

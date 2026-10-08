package custodianmock

import (
	"testing"
	"time"

	"deposit-crediting/internal/custodian"
)

func claim(id string) custodian.Claim {
	return custodian.Claim{ProviderEventID: id, ObservedAt: time.Unix(1, 0)}
}

func ids(claims []custodian.Claim) []string {
	out := make([]string, len(claims))
	for i, cl := range claims {
		out[i] = cl.ProviderEventID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Chaining two withholding options must not consume an unrelated queued claim.
// The old index-based popping removed whatever sat at the tail of ready.
func TestChainedWithholdingLeavesQueuedClaimIntact(t *testing.T) {
	c := NewMock()
	c.Observe(claim("queued"))
	c.Observe(claim("faulted"), Dropped(), Delayed())

	got := ids(c.Webhooks())
	if !equal(got, []string{"queued"}) {
		t.Fatalf("webhooks = %v, want [queued]", got)
	}
}

func TestDuplicateDeliversTwice(t *testing.T) {
	c := NewMock()
	c.Observe(claim("a"), Duplicate())

	got := ids(c.Webhooks())
	if !equal(got, []string{"a", "a"}) {
		t.Fatalf("webhooks = %v, want [a a]", got)
	}
}

func TestReorderedGoesFirst(t *testing.T) {
	c := NewMock()
	c.Observe(claim("first"))
	c.Observe(claim("second"), Reordered())

	got := ids(c.Webhooks())
	if !equal(got, []string{"second", "first"}) {
		t.Fatalf("webhooks = %v, want [second first]", got)
	}
}

func TestDelayedHeldUntilRelease(t *testing.T) {
	c := NewMock()
	c.Observe(claim("late"), Delayed())
	if got := ids(c.Webhooks()); len(got) != 0 {
		t.Fatalf("webhooks before release = %v, want empty", got)
	}
	c.ReleaseDelayed()
	if got := ids(c.Webhooks()); !equal(got, []string{"late"}) {
		t.Fatalf("webhooks after release = %v, want [late]", got)
	}
}

// A dropped claim is absent from the webhook channel but still visible to the
// query API, which is what lets the reconciler's overlap re-fetch recover it.
func TestDroppedStillInQueryTruth(t *testing.T) {
	c := NewMock()
	c.Observe(claim("gone"), Dropped())
	if got := ids(c.Webhooks()); len(got) != 0 {
		t.Fatalf("webhooks = %v, want empty", got)
	}
	claims, err := c.FetchDeposits(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("FetchDeposits: %v", err)
	}
	if !equal(ids(claims), []string{"gone"}) {
		t.Fatalf("query truth = %v, want [gone]", ids(claims))
	}
}

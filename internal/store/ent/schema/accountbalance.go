package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// timeNow is a variable so tests can reason about determinism and ent can
// share one default across schemas.
var timeNow = time.Now

// AccountBalance is the materialized current balance per (account,
// asset), maintained in the same transaction as its ledger entries
// (ADR 0003). Flagged blocks further debits after a reversal drove the
// balance negative (ADR 0002). Version supports optimistic concurrency on
// hot platform accounts.
type AccountBalance struct {
	ent.Schema
}

// Fields of the AccountBalance.
func (AccountBalance) Fields() []ent.Field {
	return []ent.Field{
		field.String("account").NotEmpty(),
		field.String("asset").NotEmpty(),
		field.String("balance").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Default("0"),
		// Held is the portion credited but not spendable under an
		// exposure-cap hold (risk-policy §4). Spendable = balance - held.
		field.String("held").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Default("0"),
		field.Bool("flagged").Default(false),
		field.Int("version").Default(0),
	}
}

// Indexes of the AccountBalance.
func (AccountBalance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account", "asset").Unique(),
	}
}

package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Deposit is one logical creditable transfer and its lifecycle state
// (ADR 0005). One row per logical transfer ID, regardless of which mode
// observed it (ADR 0001). Block facts describe current canonical
// inclusion and may change; the transfer ID never does.
type Deposit struct {
	ent.Schema
}

// Fields of the Deposit.
func (Deposit) Fields() []ent.Field {
	return []ent.Field{
		field.String("transfer_id").NotEmpty().Unique().Immutable(),
		field.String("chain").NotEmpty(),
		field.String("asset").NotEmpty(),
		field.String("account").NotEmpty(),
		field.String("address").NotEmpty(),
		field.String("amount").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}),
		field.Enum("mode").Values("self_built", "custodian"),
		field.Enum("state").
			Values("PENDING", "CREDITED", "FINALIZED", "REORGED", "DROPPED", "REVERSED", "BELOW_MINIMUM").
			Default("PENDING"),
		field.Int("credit_cycle").Default(0),
		field.Int64("block_height").Optional().Nillable(),
		field.String("block_hash").Optional().Nillable(),
		// Canonical head height when the reorg was detected; start of the
		// reorg window countdown.
		field.Int64("reorged_height").Optional().Nillable(),
		// Transaction hash carrying the transfer; lets the custodian
		// re-checker locate a re-included transfer (TxByHash).
		field.String("tx_hash").Optional().Nillable(),
		// Originating custodian source event (provider:provider_event_id)
		// for audit; empty in self-built mode.
		field.String("source_event").Optional().Nillable(),
		// Held means the credit posted but is not spendable: an exposure
		// cap hold was active at credit time (risk-policy §4).
		field.Bool("held").Default(false),
		field.Time("created_at").Default(timeNow).Immutable(),
		field.Time("updated_at").Default(timeNow).UpdateDefault(timeNow),
	}
}

// Indexes of the Deposit.
func (Deposit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account", "asset"),
		index.Fields("state"),
		index.Fields("chain", "state", "block_height"), // finalization queries
		index.Fields("tx_hash"),                        // custodian re-checker lookup
		index.Fields("state", "reorged_height"),        // reorg window expiry
	}
}

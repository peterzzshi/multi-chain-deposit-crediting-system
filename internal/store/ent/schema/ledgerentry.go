package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LedgerEntry is one append-only credit, debit, or reversal in integer
// base units (ADR 0003). History is never edited or deleted; a reversal
// is a new entry referencing the original credit's ref (ADR 0002).
type LedgerEntry struct {
	ent.Schema
}

// Fields of the LedgerEntry.
func (LedgerEntry) Fields() []ent.Field {
	return []ent.Field{
		field.String("account").NotEmpty(),
		field.String("asset").NotEmpty(),
		field.Enum("type").Values("credit", "debit", "reversal"),
		field.String("amount").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}),
		field.String("ref").NotEmpty().Unique().Immutable(),
		field.Time("created_at").Default(timeNow).Immutable(),
	}
}

// Indexes of the LedgerEntry.
func (LedgerEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account", "asset"),
	}
}

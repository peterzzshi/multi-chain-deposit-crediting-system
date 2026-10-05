package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DepositAddress maps the single deposit address for an account and chain.
type DepositAddress struct {
	ent.Schema
}

// Fields of the DepositAddress.
func (DepositAddress) Fields() []ent.Field {
	return []ent.Field{
		field.String("account").NotEmpty(),
		field.String("chain").NotEmpty(),
		field.String("address").NotEmpty(),
		field.Bool("active").Default(true),
	}
}

// Indexes of the DepositAddress.
func (DepositAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("account", "chain").Unique(),
		index.Fields("address").Unique(), // scanner lookups
		index.Fields("chain", "address"), // other queries
	}
}

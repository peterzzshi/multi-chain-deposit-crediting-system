package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DepositAddress maps a deposit address to its owner. An address belongs
// to exactly one (account, chain, mode); a user may hold one self-built
// and one custodian address per chain (docs/assumptions-and-scope.md).
type DepositAddress struct {
	ent.Schema
}

// Fields of the DepositAddress.
func (DepositAddress) Fields() []ent.Field {
	return []ent.Field{
		field.String("account").NotEmpty(),
		field.String("chain").NotEmpty(),
		field.String("address").NotEmpty(),
		field.Enum("mode").Values("self_built", "custodian"),
		field.Bool("active").Default(true),
	}
}

// Indexes of the DepositAddress.
func (DepositAddress) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chain", "address").Unique(),
	}
}

package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CanonicalBlock records every processed block's hash, so a reorg walk-back
// can locate the fork.
type CanonicalBlock struct {
	ent.Schema
}

// Fields of the CanonicalBlock.
func (CanonicalBlock) Fields() []ent.Field {
	return []ent.Field{
		field.String("chain").NotEmpty(),
		field.Int64("height").NonNegative(),
		field.String("hash").NotEmpty(),
	}
}

// Indexes of the CanonicalBlock.
func (CanonicalBlock) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chain", "height").Unique(),
		index.Fields("chain"), // reorg walk-back queries
	}
}

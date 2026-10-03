package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// ChainCursor is the scanner's durable position on a chain: the last
// processed canonical block.
type ChainCursor struct {
	ent.Schema
}

// Fields of the ChainCursor.
func (ChainCursor) Fields() []ent.Field {
	return []ent.Field{
		field.String("chain").NotEmpty().Unique(),
		field.Int64("height").NonNegative(),
		field.String("hash").NotEmpty(),
	}
}

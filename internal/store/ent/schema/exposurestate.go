package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ExposureState is the risk monitor's projection per (chain, asset): the
// current unfinalized spendable exposure and whether new credits are
// spendability-held. The credit path reads holds_active at credit time;
// the monitor recomputes it each tick (risk-policy §4).
type ExposureState struct {
	ent.Schema
}

func (ExposureState) Fields() []ent.Field {
	return []ent.Field{
		field.String("chain").NotEmpty(),
		field.String("asset").NotEmpty(),
		field.Bool("holds_active").Default(false),
		field.String("exposure").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Default("0"),
		field.Time("updated_at").Default(timeNow).UpdateDefault(timeNow),
	}
}

func (ExposureState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chain", "asset").Unique(),
	}
}

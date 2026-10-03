package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AssetConfig holds the per-(chain, asset) crediting configuration:
// custody mode, crediting minimum, and the risk-policy thresholds
// (docs/risk-policy.md). Amounts are integer base units stored as NUMERIC.
type AssetConfig struct {
	ent.Schema
}

// Fields of the AssetConfig.
func (AssetConfig) Fields() []ent.Field {
	return []ent.Field{
		field.String("chain").NotEmpty(),
		field.String("asset").NotEmpty(),
		field.Int("decimals").NonNegative(),
		field.Enum("mode").Values("self_built", "custodian"),
		field.String("min_amount").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Default("0"),
		field.Int("n_credit").Positive(),
		field.Int("n_finalize").Positive(),
		field.Int("reorg_window").Positive(),
		// Exposure cap E_max in base units; null disables enforcement
		// (risk-policy §2/§4).
		field.String("exposure_cap").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Optional().Nillable(),
		// Credits at or above the tier are spendability-held while the cap
		// is breached; null holds all credits during a breach.
		field.String("tier_amount").
			SchemaType(map[string]string{dialect.Postgres: "numeric(78,0)"}).
			Optional().Nillable(),
	}
}

// Indexes of the AssetConfig.
func (AssetConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("chain", "asset").Unique(),
	}
}

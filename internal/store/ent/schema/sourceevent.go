package schema

import (
	"encoding/json"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SourceEvent is a raw custodian claim exactly as delivered; the unique
// (provider, provider_event_id) pair is the ingest dedup boundary
// (ADR 0001 first layer).
type SourceEvent struct {
	ent.Schema
}

func (SourceEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider").NotEmpty(),
		field.String("provider_event_id").NotEmpty(),
		field.JSON("payload", json.RawMessage{}).Optional(),
		field.Time("received_at").Default(time.Now).Immutable(),
	}
}

func (SourceEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "provider_event_id").Unique(),
	}
}

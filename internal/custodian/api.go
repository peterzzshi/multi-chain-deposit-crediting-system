package custodian

import (
	"fmt"
	"math/big"
	"time"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/domain"
)

type ClaimJSON struct {
	ProviderEventID string `json:"providerEventId"`
	Chain           string `json:"chain"`
	TxHash          string `json:"txHash"`
	To              string `json:"to"`
	Asset           string `json:"asset"`
	Amount          string `json:"amount"`
	Kind            string `json:"kind,omitempty"`
	LogIndex        *int   `json:"logIndex,omitempty"`
	TraceIndex      *int   `json:"traceIndex,omitempty"`
	ObservedAt      string `json:"observedAt"`
}

func (c ClaimJSON) ToClaim() (Claim, error) {
	amount, ok := new(big.Int).SetString(c.Amount, 10)
	if !ok {
		return Claim{}, fmt.Errorf("amount must be a base-10 integer string")
	}
	observedAt := time.Now()
	if c.ObservedAt != "" {
		parsed, err := time.Parse(time.RFC3339, c.ObservedAt)
		if err != nil {
			return Claim{}, fmt.Errorf("observedAt must be RFC3339")
		}
		observedAt = parsed
	}
	return Claim{
		ProviderEventID: c.ProviderEventID,
		Chain:           domain.NetworkID(c.Chain),
		TxHash:          c.TxHash,
		To:              c.To,
		Asset:           c.Asset,
		Amount:          amount,
		Kind:            adapters.TransferKind(c.Kind),
		LogIndex:        c.LogIndex,
		TraceIndex:      c.TraceIndex,
		ObservedAt:      observedAt,
	}, nil
}

func FromClaim(cl Claim) ClaimJSON {
	return ClaimJSON{
		ProviderEventID: cl.ProviderEventID,
		Chain:           string(cl.Chain),
		TxHash:          cl.TxHash,
		To:              cl.To,
		Asset:           cl.Asset,
		Amount:          cl.Amount.String(),
		Kind:            string(cl.Kind),
		LogIndex:        cl.LogIndex,
		TraceIndex:      cl.TraceIndex,
		ObservedAt:      cl.ObservedAt.UTC().Format(time.RFC3339),
	}
}

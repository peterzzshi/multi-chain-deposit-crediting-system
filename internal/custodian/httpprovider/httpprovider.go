// Package httpprovider implements custodian.Provider against the
// custodian stub API (cmd/custodianstub). A real custodian adapter would
// implement the same interface against the provider's query API.
package httpprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/custodian"
)

type Provider struct {
	baseURL string
	hc      *http.Client
}

func New(baseURL string) *Provider {
	return &Provider{baseURL: baseURL, hc: &http.Client{Timeout: 10 * time.Second}}
}

type claimJSON struct {
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

func (c claimJSON) claim() (custodian.Claim, error) {
	amount, ok := new(big.Int).SetString(c.Amount, 10)
	if !ok {
		return custodian.Claim{}, fmt.Errorf("httpprovider: bad amount %q", c.Amount)
	}
	observedAt, err := time.Parse(time.RFC3339, c.ObservedAt)
	if err != nil {
		return custodian.Claim{}, fmt.Errorf("httpprovider: bad observedAt %q", c.ObservedAt)
	}
	return custodian.Claim{ProviderEventID: c.ProviderEventID, Chain: c.Chain, TxHash: c.TxHash,
		To: c.To, Asset: c.Asset, Amount: amount, Kind: chain.TransferKind(c.Kind),
		LogIndex: c.LogIndex, TraceIndex: c.TraceIndex, ObservedAt: observedAt}, nil
}

func (p *Provider) FetchDeposits(ctx context.Context, since time.Time) ([]custodian.Claim, error) {
	var resp struct {
		Deposits []claimJSON `json:"deposits"`
	}
	if err := p.get(ctx, "/v1/deposits?since="+url.QueryEscape(since.UTC().Format(time.RFC3339)), &resp); err != nil {
		return nil, err
	}
	claims := make([]custodian.Claim, 0, len(resp.Deposits))
	for _, cj := range resp.Deposits {
		cl, err := cj.claim()
		if err != nil {
			return nil, err
		}
		claims = append(claims, cl)
	}
	return claims, nil
}

func (p *Provider) FetchVaultTotal(ctx context.Context, chain, asset string) (*big.Int, error) {
	var resp struct {
		Total string `json:"total"`
	}
	if err := p.get(ctx, "/v1/vaults/"+chain+"/"+asset, &resp); err != nil {
		return nil, err
	}
	total, ok := new(big.Int).SetString(resp.Total, 10)
	if !ok {
		return nil, fmt.Errorf("httpprovider: bad vault total %q", resp.Total)
	}
	return total, nil
}

func (p *Provider) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("httpprovider: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("httpprovider: GET %s: status %d: %s", path, resp.StatusCode, body.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

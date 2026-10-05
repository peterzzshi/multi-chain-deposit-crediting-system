package custodian

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

type HTTPProvider struct {
	baseURL string
	hc      *http.Client
}

func NewHTTPProvider(baseURL string) *HTTPProvider {
	return &HTTPProvider{baseURL: baseURL, hc: &http.Client{Timeout: 10 * time.Second}}
}

func (p *HTTPProvider) FetchDeposits(ctx context.Context, since time.Time) ([]Claim, error) {
	var resp struct {
		Deposits []ClaimJSON `json:"deposits"`
	}
	if err := p.get(ctx, "/v1/deposits?since="+url.QueryEscape(since.UTC().Format(time.RFC3339)), &resp); err != nil {
		return nil, err
	}
	claims := make([]Claim, 0, len(resp.Deposits))
	for _, cj := range resp.Deposits {
		cl, err := cj.ToClaim()
		if err != nil {
			return nil, err
		}
		claims = append(claims, cl)
	}
	return claims, nil
}

func (p *HTTPProvider) FetchVaultTotal(ctx context.Context, chain, asset string) (*big.Int, error) {
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

func (p *HTTPProvider) get(ctx context.Context, path string, out any) error {
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

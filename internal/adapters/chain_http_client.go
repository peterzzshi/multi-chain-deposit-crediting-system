package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type HTTPClient struct {
	baseURL string
	hc      *http.Client
}

func New(baseURL string) *HTTPClient {
	return &HTTPClient{baseURL: baseURL, hc: &http.Client{Timeout: 10 * time.Second}}
}

func (c *HTTPClient) Head(ctx context.Context) (uint64, error) {
	var head uint64
	return head, c.get(ctx, "/v1/head", &head)
}

func (c *HTTPClient) BlockHash(ctx context.Context, height uint64) (string, bool, error) {
	var hash string
	status, err := c.getRaw(ctx, fmt.Sprintf("/v1/blocks/%d/hash", height), &hash)
	if err != nil {
		return "", false, err
	}
	if status == http.StatusNotFound {
		return "", false, nil
	}
	return hash, true, nil
}

func (c *HTTPClient) Block(ctx context.Context, height uint64) (Block, error) {
	var resp struct {
		Height     uint64         `json:"height"`
		Hash       string         `json:"hash"`
		ParentHash string         `json:"parentHash"`
		Transfers  []TransferJSON `json:"transfers"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/blocks/%d", height), &resp); err != nil {
		return Block{}, err
	}
	b := Block{Height: resp.Height, Hash: resp.Hash, ParentHash: resp.ParentHash}
	for _, tj := range resp.Transfers {
		t, err := tj.ToTransfer()
		if err != nil {
			return Block{}, err
		}
		b.Transfers = append(b.Transfers, t)
	}
	return b, nil
}

func (c *HTTPClient) TxByHash(ctx context.Context, txHash string) (TxLocation, bool, error) {
	var resp struct {
		Height    uint64         `json:"height"`
		Hash      string         `json:"hash"`
		Transfers []TransferJSON `json:"transfers"`
	}
	status, err := c.getRaw(ctx, "/v1/txs/"+txHash, &resp)
	if err != nil {
		return TxLocation{}, false, err
	}
	if status == http.StatusNotFound {
		return TxLocation{}, false, nil
	}
	loc := TxLocation{Height: resp.Height, Hash: resp.Hash}
	for _, tj := range resp.Transfers {
		t, err := tj.ToTransfer()
		if err != nil {
			return TxLocation{}, false, err
		}
		loc.Transfers = append(loc.Transfers, t)
	}
	return loc, true, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, out any) error {
	status, err := c.getRaw(ctx, path, out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("httpclient: GET %s: status %d", path, status)
	}
	return nil
}

func (c *HTTPClient) getRaw(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("httpclient: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, fmt.Errorf("httpclient: GET %s: status %d: %s", path, resp.StatusCode, body.Error)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

// Package httpclient implements chain.Client against the stub node API
// (internal/adapters/chain/chainstub). A real chain adapter would
// implement the same interface over JSON-RPC.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"deposit-crediting/internal/adapters/chain"
)

type Client struct {
	baseURL string
	hc      *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, hc: &http.Client{Timeout: 10 * time.Second}}
}

type transferJSON struct {
	Kind       chain.TransferKind `json:"kind"`
	TxHash     string             `json:"txHash"`
	From       string             `json:"from"`
	To         string             `json:"to"`
	Asset      string             `json:"asset"`
	Amount     string             `json:"amount"`
	LogIndex   int                `json:"logIndex"`
	TraceIndex int                `json:"traceIndex"`
}

func (t transferJSON) transfer() (chain.Transfer, error) {
	amount, ok := new(big.Int).SetString(t.Amount, 10)
	if !ok {
		return chain.Transfer{}, fmt.Errorf("httpclient: bad amount %q", t.Amount)
	}
	return chain.Transfer{Kind: t.Kind, TxHash: t.TxHash, From: t.From, To: t.To,
		Asset: t.Asset, Amount: amount, LogIndex: t.LogIndex, TraceIndex: t.TraceIndex}, nil
}

func (c *Client) Head(ctx context.Context) (uint64, error) {
	var head uint64
	return head, c.get(ctx, "/v1/head", &head)
}

func (c *Client) BlockHash(ctx context.Context, height uint64) (string, bool, error) {
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

func (c *Client) Block(ctx context.Context, height uint64) (chain.Block, error) {
	var resp struct {
		Height     uint64         `json:"height"`
		Hash       string         `json:"hash"`
		ParentHash string         `json:"parentHash"`
		Transfers  []transferJSON `json:"transfers"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/blocks/%d", height), &resp); err != nil {
		return chain.Block{}, err
	}
	b := chain.Block{Height: resp.Height, Hash: resp.Hash, ParentHash: resp.ParentHash}
	for _, tj := range resp.Transfers {
		t, err := tj.transfer()
		if err != nil {
			return chain.Block{}, err
		}
		b.Transfers = append(b.Transfers, t)
	}
	return b, nil
}

func (c *Client) TxByHash(ctx context.Context, txHash string) (chain.TxLocation, bool, error) {
	var resp struct {
		Height    uint64         `json:"height"`
		Hash      string         `json:"hash"`
		Transfers []transferJSON `json:"transfers"`
	}
	status, err := c.getRaw(ctx, "/v1/txs/"+txHash, &resp)
	if err != nil {
		return chain.TxLocation{}, false, err
	}
	if status == http.StatusNotFound {
		return chain.TxLocation{}, false, nil
	}
	loc := chain.TxLocation{Height: resp.Height, Hash: resp.Hash}
	for _, tj := range resp.Transfers {
		t, err := tj.transfer()
		if err != nil {
			return chain.TxLocation{}, false, err
		}
		loc.Transfers = append(loc.Transfers, t)
	}
	return loc, true, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	status, err := c.getRaw(ctx, path, out)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("httpclient: GET %s: status %d", path, status)
	}
	return nil
}

func (c *Client) getRaw(ctx context.Context, path string, out any) (int, error) {
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

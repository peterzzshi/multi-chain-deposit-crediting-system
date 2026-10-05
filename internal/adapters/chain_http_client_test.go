package adapters_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"deposit-crediting/internal/adapters"
)

func setup(t *testing.T) (*adapters.Chain, adapters.Client, string) {
	t.Helper()
	c := adapters.NewChain()
	srv := httptest.NewServer(adapters.Handler(c))
	t.Cleanup(srv.Close)
	return c, adapters.New(srv.URL), srv.URL
}

func post(t *testing.T, base, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRoundtrip(t *testing.T) {
	c, client, _ := setup(t)
	ctx := context.Background()

	c.AddBlock()
	c.AddBlock(adapters.Transfer{Kind: adapters.Native, TxHash: "0xt1", To: "0xaaa", Asset: "ETH", Amount: big.NewInt(100)})

	head, err := client.Head(ctx)
	if err != nil || head != 2 {
		t.Fatalf("Head() = %d, %v; want 2, nil", head, err)
	}

	hash, found, err := client.BlockHash(ctx, 2)
	if err != nil || !found {
		t.Fatalf("BlockHash() = found %v, %v; want true, nil", found, err)
	}
	if _, found, err := client.BlockHash(ctx, 99); err != nil || found {
		t.Errorf("BlockHash(99) = found %v, %v; want false, nil", found, err)
	}
	b, err := client.Block(ctx, 2)
	if err != nil {
		t.Fatalf("Block() error: %v", err)
	}
	if b.Hash != hash || b.ParentHash == "" || b.Height != 2 {
		t.Errorf("Block() = %+v; want height 2 with matching hash", b)
	}
	if len(b.Transfers) != 1 || b.Transfers[0].To != "0xaaa" || b.Transfers[0].Amount.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("Block() transfers = %+v; want one 100 ETH transfer to 0xaaa", b.Transfers)
	}

	loc, found, err := client.TxByHash(ctx, "0xt1")
	if err != nil || !found {
		t.Fatalf("TxByHash(0xt1) = found %v, %v; want true, nil", found, err)
	}
	if loc.Height != 2 || loc.Hash != hash || len(loc.Transfers) != 1 {
		t.Errorf("TxByHash() = %+v; want block 2 location", loc)
	}

	if _, found, err := client.TxByHash(ctx, "0xnope"); err != nil || found {
		t.Errorf("TxByHash(unknown) = found %v, %v; want false, nil", found, err)
	}
}

func TestReorgChangesHashes(t *testing.T) {
	c, client, _ := setup(t)
	ctx := context.Background()

	c.AddBlock()
	c.AddBlock(adapters.Transfer{Kind: adapters.Native, TxHash: "0xt1", To: "0xaaa", Asset: "ETH", Amount: big.NewInt(100)})
	before, _, err := client.BlockHash(ctx, 2)
	if err != nil {
		t.Fatalf("BlockHash() error: %v", err)
	}

	c.Reorg(1)
	c.AddBlock() // replacement branch, no transfer

	after, _, err := client.BlockHash(ctx, 2)
	if err != nil {
		t.Fatalf("BlockHash() after reorg error: %v", err)
	}
	if before == after {
		t.Errorf("hash at height 2 unchanged across reorg: %s", before)
	}
	if _, found, err := client.TxByHash(ctx, "0xt1"); err != nil || found {
		t.Errorf("TxByHash(orphaned) = found %v, %v; want false, nil", found, err)
	}
}

func TestAdminEndpoints(t *testing.T) {
	_, client, base := setup(t)
	ctx := context.Background()

	resp := post(t, base, "/admin/blocks", map[string]any{
		"transfers": []map[string]any{
			{"kind": "native", "txHash": "0xta", "to": "0xbbb", "asset": "ETH", "amount": "50"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/blocks status = %d; want 200", resp.StatusCode)
	}
	b, err := client.Block(ctx, 1)
	if err != nil || len(b.Transfers) != 1 || b.Transfers[0].TxHash != "0xta" {
		t.Fatalf("Block(1) = %+v, %v; want the posted transfer", b, err)
	}

	resp = post(t, base, "/admin/mine", map[string]any{"count": 3})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/mine status = %d; want 200", resp.StatusCode)
	}
	head, err := client.Head(ctx)
	if err != nil || head != 4 {
		t.Fatalf("Head() = %d, %v; want 4, nil", head, err)
	}

	resp = post(t, base, "/admin/reorg", map[string]any{"depth": 2})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/reorg status = %d; want 200", resp.StatusCode)
	}
	head, err = client.Head(ctx)
	if err != nil || head != 2 {
		t.Fatalf("Head() after reorg = %d, %v; want 2, nil", head, err)
	}
}

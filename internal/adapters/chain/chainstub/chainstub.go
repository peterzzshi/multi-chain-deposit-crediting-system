// Package chainstub exposes a chaintest.Chain over HTTP, standing in for
// a chain node in local runs. Blocks are mined explicitly via the admin
// endpoints, so reorgs and confirmation depth stay fully deterministic.
package chainstub

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/adapters/chain/chaintest"
)

type transferJSON struct {
	Kind       chain.TransferKind `json:"kind"`
	TxHash     string             `json:"txHash"`
	From       string             `json:"from,omitempty"`
	To         string             `json:"to"`
	Asset      string             `json:"asset"`
	Amount     string             `json:"amount"`
	LogIndex   int                `json:"logIndex,omitempty"`
	TraceIndex int                `json:"traceIndex,omitempty"`
}

type blockJSON struct {
	Height     uint64         `json:"height"`
	Hash       string         `json:"hash"`
	ParentHash string         `json:"parentHash"`
	Transfers  []transferJSON `json:"transfers"`
}

func toJSON(t chain.Transfer) transferJSON {
	amount := "0"
	if t.Amount != nil {
		amount = t.Amount.String()
	}
	return transferJSON{Kind: t.Kind, TxHash: t.TxHash, From: t.From, To: t.To,
		Asset: t.Asset, Amount: amount, LogIndex: t.LogIndex, TraceIndex: t.TraceIndex}
}

func fromJSON(t transferJSON) (chain.Transfer, error) {
	amount, ok := new(big.Int).SetString(t.Amount, 10)
	if !ok {
		return chain.Transfer{}, errors.New("amount must be a base-10 integer string")
	}
	return chain.Transfer{Kind: t.Kind, TxHash: t.TxHash, From: t.From, To: t.To,
		Asset: t.Asset, Amount: amount, LogIndex: t.LogIndex, TraceIndex: t.TraceIndex}, nil
}

// Handler serves the stub node API and the admin controls on one mux.
func Handler(c *chaintest.Chain) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/head", func(w http.ResponseWriter, r *http.Request) {
		head, err := c.Head(r.Context())
		respond(w, head, err)
	})

	mux.HandleFunc("GET /v1/blocks/{height}/hash", func(w http.ResponseWriter, r *http.Request) {
		height, ok := pathHeight(w, r)
		if !ok {
			return
		}
		hash, err := c.BlockHash(r.Context(), height)
		respond(w, hash, err)
	})

	mux.HandleFunc("GET /v1/blocks/{height}", func(w http.ResponseWriter, r *http.Request) {
		height, ok := pathHeight(w, r)
		if !ok {
			return
		}
		b, err := c.Block(r.Context(), height)
		if err != nil {
			respond(w, nil, err)
			return
		}
		respond(w, blockToJSON(b), nil)
	})

	mux.HandleFunc("GET /v1/txs/{txHash}", func(w http.ResponseWriter, r *http.Request) {
		loc, found, err := c.TxByHash(r.Context(), r.PathValue("txHash"))
		if err != nil {
			respond(w, nil, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transaction not on canonical chain"})
			return
		}
		var transfers []transferJSON
		for _, t := range loc.Transfers {
			transfers = append(transfers, toJSON(t))
		}
		writeJSON(w, http.StatusOK, map[string]any{"height": loc.Height, "hash": loc.Hash, "transfers": transfers})
	})

	mux.HandleFunc("POST /admin/blocks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Transfers []transferJSON `json:"transfers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var transfers []chain.Transfer
		for _, tj := range req.Transfers {
			t, err := fromJSON(tj)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			transfers = append(transfers, t)
		}
		writeJSON(w, http.StatusOK, blockToJSON(c.AddBlock(transfers...)))
	})

	mux.HandleFunc("POST /admin/mine", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count int `json:"count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Count < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count must be a positive integer"})
			return
		}
		var head chain.Block
		for i := 0; i < req.Count; i++ {
			head = c.AddBlock()
		}
		writeJSON(w, http.StatusOK, map[string]uint64{"height": head.Height})
	})

	mux.HandleFunc("POST /admin/reorg", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Depth int `json:"depth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Depth < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "depth must be a positive integer"})
			return
		}
		c.Reorg(req.Depth)
		head, _ := c.Head(r.Context())
		writeJSON(w, http.StatusOK, map[string]uint64{"height": head})
	})

	return mux
}

func blockToJSON(b chain.Block) blockJSON {
	out := blockJSON{Height: b.Height, Hash: b.Hash, ParentHash: b.ParentHash}
	for _, t := range b.Transfers {
		out.Transfers = append(out.Transfers, toJSON(t))
	}
	return out
}

func pathHeight(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	height, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "height must be a non-negative integer"})
		return 0, false
	}
	return height, true
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "no canonical block") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

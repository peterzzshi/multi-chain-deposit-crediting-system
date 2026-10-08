package chaintest

import (
	"encoding/json"
	"net/http"
	"strconv"

	"deposit-crediting/internal/adapters"
)

func Handler(c *Chain) http.Handler {
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
		hash, found, err := c.BlockHash(r.Context(), height)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no canonical block at height"})
			return
		}
		respond(w, hash, nil)
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
		var transfers []adapters.TransferJSON
		for _, t := range loc.Transfers {
			transfers = append(transfers, adapters.FromTransfer(t))
		}
		writeJSON(w, http.StatusOK, map[string]any{"height": loc.Height, "hash": loc.Hash, "transfers": transfers})
	})

	mux.HandleFunc("POST /admin/blocks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Transfers []adapters.TransferJSON `json:"transfers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var transfers []adapters.Transfer
		for _, tj := range req.Transfers {
			t, err := tj.ToTransfer()
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
		var head adapters.Block
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

type blockJSON struct {
	Height     uint64                  `json:"height"`
	Hash       string                  `json:"hash"`
	ParentHash string                  `json:"parentHash"`
	Transfers  []adapters.TransferJSON `json:"transfers"`
}

func blockToJSON(b adapters.Block) blockJSON {
	out := blockJSON{Height: b.Height, Hash: b.Hash, ParentHash: b.ParentHash}
	for _, t := range b.Transfers {
		out.Transfers = append(out.Transfers, adapters.FromTransfer(t))
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

func respond(w http.ResponseWriter, data any, err error) {
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

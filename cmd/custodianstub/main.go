// custodianstub runs a stub custodian over HTTP: a query API (deposits,
// vault totals) plus admin endpoints to observe deposits with webhook
// faults (duplicate/reordered/delayed/dropped). Draining /admin/webhooks
// yields the deliveries to POST at the app's webhook endpoint — the
// "push" channel is your curl. See docs/manual-verification.md.
package main

import (
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/custodian/custodiantest"
)

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

func toJSON(cl custodian.Claim) claimJSON {
	return claimJSON{ProviderEventID: cl.ProviderEventID, Chain: cl.Chain, TxHash: cl.TxHash,
		To: cl.To, Asset: cl.Asset, Amount: cl.Amount.String(), Kind: string(cl.Kind),
		LogIndex: cl.LogIndex, TraceIndex: cl.TraceIndex,
		ObservedAt: cl.ObservedAt.UTC().Format(time.RFC3339)}
}

func fromJSON(cj claimJSON) (custodian.Claim, error) {
	amount, ok := new(big.Int).SetString(cj.Amount, 10)
	if !ok {
		return custodian.Claim{}, errString("amount must be a base-10 integer string")
	}
	observedAt := time.Now()
	if cj.ObservedAt != "" {
		parsed, err := time.Parse(time.RFC3339, cj.ObservedAt)
		if err != nil {
			return custodian.Claim{}, errString("observedAt must be RFC3339")
		}
		observedAt = parsed
	}
	return custodian.Claim{ProviderEventID: cj.ProviderEventID, Chain: cj.Chain, TxHash: cj.TxHash,
		To: cj.To, Asset: cj.Asset, Amount: amount, Kind: chain.TransferKind(cj.Kind),
		LogIndex: cj.LogIndex, TraceIndex: cj.TraceIndex, ObservedAt: observedAt}, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	c := custodiantest.New()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/deposits", func(w http.ResponseWriter, r *http.Request) {
		since := time.Time{}
		if raw := r.URL.Query().Get("since"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since must be RFC3339"})
				return
			}
			since = parsed
		}
		claims, err := c.FetchDeposits(r.Context(), since)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]claimJSON, 0, len(claims))
		for _, cl := range claims {
			out = append(out, toJSON(cl))
		}
		writeJSON(w, http.StatusOK, map[string]any{"deposits": out})
	})

	mux.HandleFunc("GET /v1/vaults/{chain}/{asset}", func(w http.ResponseWriter, r *http.Request) {
		total, err := c.FetchVaultTotal(r.Context(), r.PathValue("chain"), r.PathValue("asset"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"total": total.String()})
	})

	mux.HandleFunc("POST /admin/deposits", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			claimJSON
			Faults []string `json:"faults"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cl, err := fromJSON(req.claimJSON)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var opts []custodiantest.ObserveOption
		for _, fault := range req.Faults {
			switch fault {
			case "duplicate":
				opts = append(opts, custodiantest.Duplicate())
			case "reordered":
				opts = append(opts, custodiantest.Reordered())
			case "delayed":
				opts = append(opts, custodiantest.Delayed())
			case "dropped":
				opts = append(opts, custodiantest.Dropped())
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown fault " + fault})
				return
			}
		}
		c.Observe(cl, opts...)
		writeJSON(w, http.StatusOK, map[string]string{"status": "observed"})
	})

	mux.HandleFunc("POST /admin/vaults", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Chain string `json:"chain"`
			Asset string `json:"asset"`
			Total string `json:"total"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		total, ok := new(big.Int).SetString(req.Total, 10)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "total must be a base-10 integer string"})
			return
		}
		c.SetVaultTotal(req.Chain, req.Asset, total)
		writeJSON(w, http.StatusOK, map[string]string{"status": "set"})
	})

	mux.HandleFunc("GET /admin/webhooks", func(w http.ResponseWriter, r *http.Request) {
		claims := c.Webhooks()
		out := make([]claimJSON, 0, len(claims))
		for _, cl := range claims {
			out = append(out, toJSON(cl))
		}
		writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
	})

	mux.HandleFunc("POST /admin/release-delayed", func(w http.ResponseWriter, _ *http.Request) {
		c.ReleaseDelayed()
		writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
	})

	addr := env("LISTEN_ADDR", ":9300")
	slog.Info("custodianstub listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("custodianstub exited", "err", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

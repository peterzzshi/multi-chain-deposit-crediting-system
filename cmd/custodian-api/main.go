package main

import (
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"time"

	"deposit-crediting/internal/config"
	"deposit-crediting/internal/custodian"
)

type faultKind string

const (
	faultDuplicate faultKind = "duplicate"
	faultReordered faultKind = "reordered"
	faultDelayed   faultKind = "delayed"
	faultDropped   faultKind = "dropped"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	c := custodian.NewMock()
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
		out := make([]custodian.ClaimJSON, 0, len(claims))
		for _, cl := range claims {
			out = append(out, custodian.FromClaim(cl))
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
			custodian.ClaimJSON
			Faults []faultKind `json:"faults"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cl, err := req.ClaimJSON.ToClaim()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var opts []custodian.ObserveOption
		for _, fault := range req.Faults {
			switch fault {
			case faultDuplicate:
				opts = append(opts, custodian.Duplicate())
			case faultReordered:
				opts = append(opts, custodian.Reordered())
			case faultDelayed:
				opts = append(opts, custodian.Delayed())
			case faultDropped:
				opts = append(opts, custodian.Dropped())
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown fault " + string(fault)})
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
		out := make([]custodian.ClaimJSON, 0, len(claims))
		for _, cl := range claims {
			out = append(out, custodian.FromClaim(cl))
		}
		writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
	})

	mux.HandleFunc("POST /admin/release-delayed", func(w http.ResponseWriter, _ *http.Request) {
		c.ReleaseDelayed()
		writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
	})

	addr := config.RequireEnv("LISTEN_ADDR")
	slog.Info("custodian-api listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("custodian-api exited", "err", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

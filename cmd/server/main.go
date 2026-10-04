// server exposes the custodian webhook endpoint, an external-debit
// endpoint (simulating the platform's other flows), and read endpoints
// for inspecting deposits and balances. See docs/manual-verification.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"time"

	"deposit-crediting/internal/adapters/chain"
	"deposit-crediting/internal/adapters/chain/httpclient"
	"deposit-crediting/internal/config"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	entdeposit "deposit-crediting/internal/store/ent/deposit"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	db, err := ent.Open("postgres", env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/deposit_crediting?sslmode=disable"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()
	if err := db.Schema.Create(context.Background()); err != nil {
		fatal("migrate schema", err)
	}
	assets, err := config.LoadAssets(env("ASSETS_CONFIG", "configs/assets.json"))
	if err != nil {
		fatal("load assets config", err)
	}
	if err := store.UpsertAssetConfigs(context.Background(), db, assets); err != nil {
		fatal("apply assets config", err)
	}

	engine := credit.NewEngine(store.New(db))
	ingestor := custodian.NewIngestor(custodian.Config{
		ChainID:  env("CHAIN_ID", "stubchain"),
		Provider: env("PROVIDER", "custodianA"),
	}, httpclient.New(env("CHAIN_API_URL", "http://localhost:9100")), store.NewCustodianStore(db))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/webhooks/custodian", webhookHandler(ingestor))
	mux.HandleFunc("POST /v1/debits", debitHandler(engine))
	mux.HandleFunc("GET /v1/deposits/{transferID}", depositHandler(db))
	mux.HandleFunc("GET /v1/deposits", depositsHandler(db))
	mux.HandleFunc("GET /v1/balances/{account}", balancesHandler(db))

	addr := env("LISTEN_ADDR", ":9200")
	slog.Info("server listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fatal("server exited", err)
	}
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

func webhookHandler(ing *custodian.Ingestor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cj claimJSON
		if err := json.NewDecoder(r.Body).Decode(&cj); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		amount, ok := new(big.Int).SetString(cj.Amount, 10)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount must be a base-10 integer string"})
			return
		}
		observedAt := time.Now()
		if cj.ObservedAt != "" {
			parsed, err := time.Parse(time.RFC3339, cj.ObservedAt)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "observedAt must be RFC3339"})
				return
			}
			observedAt = parsed
		}
		err := ing.Handle(r.Context(), custodian.Claim{
			ProviderEventID: cj.ProviderEventID, Chain: cj.Chain, TxHash: cj.TxHash,
			To: cj.To, Asset: cj.Asset, Amount: amount, Kind: chain.TransferKind(cj.Kind),
			LogIndex: cj.LogIndex, TraceIndex: cj.TraceIndex, ObservedAt: observedAt,
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
		case errors.Is(err, errs.ErrClaimNotOnChain):
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "not_on_chain"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
	}
}

func debitHandler(engine *credit.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Account string `json:"account"`
			Asset   string `json:"asset"`
			Amount  string `json:"amount"`
			Ref     string `json:"ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		amount, ok := new(big.Int).SetString(req.Amount, 10)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount must be a base-10 integer string"})
			return
		}
		err := engine.Debit(r.Context(), req.Account, req.Asset, amount, req.Ref)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]string{"status": "posted"})
		case errors.Is(err, errs.ErrDuplicateRef):
			writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate_ignored"})
		case errors.Is(err, errs.ErrInsufficientFunds), errors.Is(err, errs.ErrAccountFlagged):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
	}
}

type depositJSON struct {
	TransferID    string  `json:"transferID"`
	Chain         string  `json:"chain"`
	Asset         string  `json:"asset"`
	Account       string  `json:"account"`
	Address       string  `json:"address"`
	Amount        string  `json:"amount"`
	Mode          string  `json:"mode"`
	State         string  `json:"state"`
	BlockHeight   *int64  `json:"blockHeight,omitempty"`
	BlockHash     *string `json:"blockHash,omitempty"`
	TxHash        *string `json:"txHash,omitempty"`
	Held          bool    `json:"held"`
	ReorgedHeight *int64  `json:"reorgedHeight,omitempty"`
	SourceEvent   *string `json:"sourceEvent,omitempty"`
}

func toDepositJSON(d *ent.Deposit) depositJSON {
	return depositJSON{
		TransferID: d.TransferID, Chain: d.Chain, Asset: d.Asset, Account: d.Account,
		Address: d.Address, Amount: d.Amount, Mode: string(d.Mode), State: string(d.State),
		BlockHeight: d.BlockHeight, BlockHash: d.BlockHash, TxHash: d.TxHash,
		Held: d.Held, ReorgedHeight: d.ReorgedHeight, SourceEvent: d.SourceEvent,
	}
}

func depositHandler(db *ent.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := db.Deposit.Query().
			Where(entdeposit.TransferID(r.PathValue("transferID"))).
			Only(r.Context())
		if ent.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "deposit not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, toDepositJSON(d))
	}
}

func depositsHandler(db *ent.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := db.Deposit.Query()
		if account := r.URL.Query().Get("account"); account != "" {
			query = query.Where(entdeposit.Account(account))
		}
		rows, err := query.All(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]depositJSON, 0, len(rows))
		for _, d := range rows {
			out = append(out, toDepositJSON(d))
		}
		writeJSON(w, http.StatusOK, map[string]any{"deposits": out})
	}
}

func balancesHandler(db *ent.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.AccountBalance.Query().
			Where(accountbalance.Account(r.PathValue("account"))).
			All(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		type balanceJSON struct {
			Account string `json:"account"`
			Asset   string `json:"asset"`
			Balance string `json:"balance"`
			Held    string `json:"held"`
			Flagged bool   `json:"flagged"`
		}
		out := make([]balanceJSON, 0, len(rows))
		for _, b := range rows {
			out = append(out, balanceJSON{Account: b.Account, Asset: b.Asset,
				Balance: b.Balance, Held: b.Held, Flagged: b.Flagged})
		}
		writeJSON(w, http.StatusOK, map[string]any{"balances": out})
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

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}

package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strconv"

	"deposit-crediting/internal/adapters"
	"deposit-crediting/internal/config"
	"deposit-crediting/internal/credit"
	"deposit-crediting/internal/custodian"
	"deposit-crediting/internal/domain"
	"deposit-crediting/internal/errs"
	"deposit-crediting/internal/store"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/accountbalance"
	entdeposit "deposit-crediting/internal/store/ent/deposit"

	_ "github.com/lib/pq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	db, err := ent.Open("postgres", config.RequireEnv("DATABASE_URL"))
	if err != nil {
		fatal("open database", err)
	}
	defer db.Close()

	assets, err := config.LoadAssets(config.RequireEnv("ASSETS_CONFIG"))
	if err != nil {
		fatal("load assets config", err)
	}
	assetMap, err := config.NewAssetConfigMap(assets)
	if err != nil {
		fatal("build asset config map", err)
	}

	chains, err := config.LoadChains(config.RequireEnv("CHAINS_CONFIG"))
	if err != nil {
		fatal("load chains config", err)
	}

	// Build tier amounts for all chains
	creditStore := store.New(db)
	for _, chain := range chains {
		chainAssets := assetMap.ForChain(chain.NetworkID)
		tiers := make(map[string]*big.Int)
		for _, ac := range chainAssets {
			if ac.TierAmount != nil {
				tiers[ac.Asset] = ac.TierAmount
			}
		}
		creditStore.SetTierAmounts(string(chain.NetworkID), tiers)
	}

	engine := credit.NewEngine(creditStore)

	// Build ingestors for all chains
	ingestors := make(map[domain.NetworkID]*custodian.Ingestor)
	for _, chain := range chains {
		custodianAssets := assetMap.ForChainAndMode(chain.NetworkID, "custodian")
		custodianAssetConfigs := make(map[string]domain.AssetPolicy, len(custodianAssets))
		for _, ac := range custodianAssets {
			custodianAssetConfigs[ac.Asset] = domain.AssetPolicy{
				Asset:       ac.Asset,
				MinAmount:   ac.MinAmount,
				NCredit:     ac.NCredit,
				NFinalize:   ac.NFinalize,
				ReorgWindow: ac.ReorgWindow,
				ExposureCap: ac.ExposureCap,
			}
		}

		ingestors[chain.NetworkID] = custodian.NewIngestor(custodian.Config{
			ChainID:  chain.NetworkID,
			Provider: config.RequireEnv("PROVIDER"),
		}, adapters.New(chain.ChainAPIURL), store.NewCustodianStore(db), engine, custodianAssetConfigs)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/webhooks/custodian", webhookHandler(ingestors))
	mux.HandleFunc("POST /v1/debits", debitHandler(engine))
	mux.HandleFunc("GET /v1/deposits/{transferID}", depositHandler(db))
	mux.HandleFunc("GET /v1/deposits", depositsHandler(db))
	mux.HandleFunc("GET /v1/balances/{account}", balancesHandler(db))

	addr := config.RequireEnv("LISTEN_ADDR")
	slog.Info("server listening", "addr", addr, "chains", len(chains))
	if err := http.ListenAndServe(addr, mux); err != nil {
		fatal("server exited", err)
	}
}

func webhookHandler(ingestors map[domain.NetworkID]*custodian.Ingestor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cj custodian.ClaimJSON
		if err := json.NewDecoder(r.Body).Decode(&cj); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cl, err := cj.ToClaim()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ing, ok := ingestors[cl.Chain]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported chain: " + string(cl.Chain)})
			return
		}
		err = ing.Handle(r.Context(), cl)
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
		case errors.Is(err, errs.ErrInsufficientFunds):
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

const (
	defaultDepositLimit = 100
	maxDepositLimit     = 1000
)

func depositsHandler(db *ent.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := db.Deposit.Query()
		if account := r.URL.Query().Get("account"); account != "" {
			query = query.Where(entdeposit.Account(account))
		}
		limit := defaultDepositLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive integer"})
				return
			}
			limit = min(n, maxDepositLimit)
		}
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offset must be a non-negative integer"})
				return
			}
			offset = n
		}
		rows, err := query.Limit(limit).Offset(offset).All(r.Context())
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
		}
		out := make([]balanceJSON, 0, len(rows))
		for _, b := range rows {
			out = append(out, balanceJSON{Account: b.Account, Asset: b.Asset,
				Balance: b.Balance, Held: b.Held})
		}
		writeJSON(w, http.StatusOK, map[string]any{"balances": out})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fatal(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}

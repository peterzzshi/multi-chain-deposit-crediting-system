package store

import (
	"context"
	"fmt"

	"deposit-crediting/internal/config"
	"deposit-crediting/internal/store/ent"
	"deposit-crediting/internal/store/ent/assetconfig"
)

// UpsertAssetConfigs applies supported-asset policy to the database.
func UpsertAssetConfigs(ctx context.Context, client *ent.Client, entries []config.AssetEntry) error {
	for _, e := range entries {
		row, err := client.AssetConfig.Query().
			Where(assetconfig.Chain(e.Chain), assetconfig.Asset(e.Asset)).
			Only(ctx)
		switch {
		case ent.IsNotFound(err):
			c := client.AssetConfig.Create().
				SetChain(e.Chain).SetAsset(e.Asset).
				SetDecimals(e.Decimals).SetMode(assetconfig.Mode(e.Mode)).
				SetMinAmount(e.MinAmount).SetNCredit(e.NCredit).
				SetNFinalize(e.NFinalize).SetReorgWindow(e.ReorgWindow)
			if e.ExposureCap != "" {
				c.SetExposureCap(e.ExposureCap)
			}
			if e.TierAmount != "" {
				c.SetTierAmount(e.TierAmount)
			}
			if _, err := c.Save(ctx); err != nil {
				return fmt.Errorf("store: insert asset config %s/%s: %w", e.Chain, e.Asset, err)
			}
		case err != nil:
			return fmt.Errorf("store: load asset config %s/%s: %w", e.Chain, e.Asset, err)
		default:
			u := row.Update().
				SetDecimals(e.Decimals).SetMode(assetconfig.Mode(e.Mode)).
				SetMinAmount(e.MinAmount).SetNCredit(e.NCredit).
				SetNFinalize(e.NFinalize).SetReorgWindow(e.ReorgWindow)
			if e.ExposureCap != "" {
				u.SetExposureCap(e.ExposureCap)
			} else {
				u.ClearExposureCap()
			}
			if e.TierAmount != "" {
				u.SetTierAmount(e.TierAmount)
			} else {
				u.ClearTierAmount()
			}
			if _, err := u.Save(ctx); err != nil {
				return fmt.Errorf("store: update asset config %s/%s: %w", e.Chain, e.Asset, err)
			}
		}
	}
	return nil
}

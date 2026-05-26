package exchange

// USDStablecoins enumerates the assets that exchange syncs should treat as cash
// at parity with USDT. Without this list, an account holding 5,000 USDC would
// show $0 equity because USDT-only syncs silently drop USDC, BUSD, etc. from
// both the cash bucket AND the positions list.
//
// We treat all of these at exactly 1.0 USDT. The real cross-stable peg can
// drift a few basis points (USDCUSDT ≈ 0.9998–1.0002 typically) but for
// equity display the simplification is well within the noise users care about.
// If you need fee-cent precision, replace the lookup with a per-pair ticker
// fetch — but be aware that doing so adds an exchange round-trip on every sync.
var USDStablecoins = map[string]bool{
	"USDT":  true,
	"USDC":  true,
	"BUSD":  true, // Binance USD (delisted 2024 but still appears in legacy accounts)
	"FDUSD": true, // First Digital USD (Binance launch market)
	"TUSD":  true, // TrueUSD
	"DAI":   true, // MakerDAO
	"USDP":  true, // Paxos
	"PYUSD": true, // PayPal USD
}

// IsUSDStable reports whether the asset symbol is a USD-pegged stablecoin we
// roll into the cash bucket during account sync. Symbol comparison is exact
// (case-sensitive): broker APIs uniformly return upper-case tickers.
func IsUSDStable(asset string) bool {
	return USDStablecoins[asset]
}

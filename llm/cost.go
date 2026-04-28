package llm

// Prices in USD per 1M tokens. Update alongside model changes.
type modelPrice struct{ inUSDPerM, outUSDPerM, cachedInUSDPerM float64 }

var pricing = map[string]modelPrice{
	"gpt-4.1":         {inUSDPerM: 2.00, outUSDPerM: 8.00, cachedInUSDPerM: 0.50},
	"gpt-4.1-mini":    {inUSDPerM: 0.40, outUSDPerM: 1.60, cachedInUSDPerM: 0.10},
	"gpt-4.1-nano":    {inUSDPerM: 0.10, outUSDPerM: 0.40, cachedInUSDPerM: 0.025},
	// Add models here as they're adopted.
}

// Cost returns USD cost for one chat call. Unknown model → 0 (logged elsewhere).
func Cost(model string, inputTokens, outputTokens, cachedInputTokens int) float64 {
	p, ok := pricing[model]
	if !ok {
		return 0
	}
	billableIn := inputTokens - cachedInputTokens
	if billableIn < 0 {
		billableIn = 0
	}
	return (float64(billableIn)*p.inUSDPerM +
		float64(cachedInputTokens)*p.cachedInUSDPerM +
		float64(outputTokens)*p.outUSDPerM) / 1_000_000.0
}

func KnownModel(model string) bool {
	_, ok := pricing[model]
	return ok
}

package codex

import "strings"

// ModelPricing records approximate per-token model rates.
//
// Pricing must be updated manually when OpenAI changes published rates. Last
// verified: 2026-06-23.
//
// Sources:
//   - https://openai.com/api/pricing/
//   - https://developers.openai.com/codex/pricing/
//
// OpenAI's Codex pricing page says API-key Codex usage is charged based on API
// pricing, while Codex plan credits use the same per-token rates. Unknown models
// conservatively use the highest known rate in this table.
type ModelPricing struct {
	InputPerMTok       float64
	CachedInputPerMTok float64
	OutputPerMTok      float64
}

var modelPricing = map[string]ModelPricing{
	// GPT-5.5: input $5.00 / 1M, cached input $0.50 / 1M, output $30.00 / 1M.
	"gpt-5.5": {InputPerMTok: 5.00, CachedInputPerMTok: 0.50, OutputPerMTok: 30.00},
	// GPT-5.4: input $2.50 / 1M, cached input $0.25 / 1M, output $15.00 / 1M.
	"gpt-5.4": {InputPerMTok: 2.50, CachedInputPerMTok: 0.25, OutputPerMTok: 15.00},
	// GPT-5.4 mini: input $0.75 / 1M, cached input $0.075 / 1M, output $4.50 / 1M.
	"gpt-5.4-mini": {InputPerMTok: 0.75, CachedInputPerMTok: 0.075, OutputPerMTok: 4.50},
	"gpt-5.4 mini": {InputPerMTok: 0.75, CachedInputPerMTok: 0.075, OutputPerMTok: 4.50},
}

var unknownModelPricing = ModelPricing{InputPerMTok: 5.00, CachedInputPerMTok: 0.50, OutputPerMTok: 30.00}

func pricingForModel(model string) ModelPricing {
	model = strings.ToLower(strings.TrimSpace(model))
	if p, ok := modelPricing[model]; ok {
		return p
	}
	return unknownModelPricing
}

func estimateCostUSD(model string, inputTokens, cachedInputTokens, outputTokens int) float64 {
	if cachedInputTokens > inputTokens {
		cachedInputTokens = inputTokens
	}
	uncachedInput := inputTokens - cachedInputTokens
	p := pricingForModel(model)
	return (float64(uncachedInput)*p.InputPerMTok +
		float64(cachedInputTokens)*p.CachedInputPerMTok +
		float64(outputTokens)*p.OutputPerMTok) / 1_000_000
}

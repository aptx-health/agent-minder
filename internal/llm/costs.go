package llm

// ModelCost holds per-token cost in USD for a model.
type ModelCost struct {
	InputPerMTok  float64 // cost per million input tokens
	OutputPerMTok float64 // cost per million output tokens
}

// Cost returns the total cost for a given token usage.
func (c ModelCost) Cost(inputToks, outputToks int) float64 {
	return float64(inputToks)*c.InputPerMTok/1e6 + float64(outputToks)*c.OutputPerMTok/1e6
}

// knownCosts maps model IDs to their per-token costs.
// Prices as of early 2026; update as needed.
var knownCosts = map[string]ModelCost{
	// Anthropic
	"claude-haiku-4-5":  {InputPerMTok: 0.80, OutputPerMTok: 4.00},
	"claude-sonnet-4-6": {InputPerMTok: 3.00, OutputPerMTok: 15.00},
	"claude-opus-4-6":   {InputPerMTok: 15.00, OutputPerMTok: 75.00},
	// OpenAI
	"gpt-4o":       {InputPerMTok: 2.50, OutputPerMTok: 10.00},
	"gpt-4o-mini":  {InputPerMTok: 0.15, OutputPerMTok: 0.60},
	"gpt-4.1":      {InputPerMTok: 2.00, OutputPerMTok: 8.00},
	"gpt-4.1-mini": {InputPerMTok: 0.40, OutputPerMTok: 1.60},
	"gpt-4.1-nano": {InputPerMTok: 0.10, OutputPerMTok: 0.40},
}

// LookupCost returns the cost info for a model, or a zero-cost fallback if unknown.
func LookupCost(model string) ModelCost {
	if c, ok := knownCosts[model]; ok {
		return c
	}
	return ModelCost{}
}

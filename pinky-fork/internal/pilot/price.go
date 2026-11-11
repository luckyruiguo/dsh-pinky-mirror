package pilot

import "strings"

// price is what a model costs, in US dollars a million tokens. Cache
// writes are those of the 5-minute cache, the one Pinky uses.
type price struct {
	input, output, cacheRead, cacheWrite float64
	// Above longPrompt tokens a request costs long instead (Haiku 5.5).
	longPrompt int64
	long       *price
}

// prices are the Claude models' prices on Anthropic's API, by the start
// of the model's name (a dated name costs what its model does).
var prices = map[string]price{
	"claude-opus-5-5":   {input: 4, output: 20, cacheRead: 0.20, cacheWrite: 5},
	"claude-opus-5":     {input: 5, output: 25, cacheRead: 0.50, cacheWrite: 6.25},
	"claude-sonnet-5-5": {input: 2, output: 10, cacheRead: 0.10, cacheWrite: 2.50},
	"claude-sonnet-5":   {input: 2, output: 10, cacheRead: 0.20, cacheWrite: 2.50},
	"claude-fable-5-2":  {input: 8, output: 40, cacheRead: 0.20, cacheWrite: 10},
	"claude-haiku-5-5": {input: 0.10, output: 0.50, cacheRead: 0.01, cacheWrite: 0.125,
		longPrompt: 100_000, long: &price{input: 0.50, output: 2.50, cacheRead: 0.05, cacheWrite: 0.625}},
}

// priceOf returns the prices of the model of cfg, if Pinky knows them:
// for Claude models on the Messages API.
func priceOf(cfg Config) (price, bool) {
	if cfg.Provider != Anthropic {
		return price{}, false
	}
	best := ""
	for name := range prices {
		if strings.HasPrefix(cfg.Model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return price{}, false
	}
	return prices[best], true
}

// cost is what a request cost, in US dollars.
func (p price) cost(u usage) float64 {
	if p.long != nil && u.input+u.cacheRead+u.cacheWrite > p.longPrompt {
		p = *p.long
	}
	return (float64(u.input)*p.input + float64(u.output)*p.output +
		float64(u.cacheRead)*p.cacheRead + float64(u.cacheWrite)*p.cacheWrite) / 1e6
}

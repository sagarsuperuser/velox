package litellm

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestModelFamilies_EveryTokenPricedByARecipe pins mapper↔recipe coherence:
// every recipe token the mapper can emit on the `model` dimension must have
// at least one pricing rule in the matching recipe, or traffic on that
// family ingests fine and then silently doesn't bill at cycle close (the
// unclaimed-usage WARN nobody tails). This is exactly how the catalogs went
// stale once: models were added to the mapper's world (via prefix match) or
// the recipe's world independently, with nothing asserting the join.
//
// Direction matters: mapper tokens ⊆ recipe rules. The reverse (recipe
// prices a family the mapper doesn't detect) is harmless — direct API
// ingest can still use it.
func TestModelFamilies_EveryTokenPricedByARecipe(t *testing.T) {
	recipeModels := func(path string) map[string]bool {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			PricingRules []struct {
				DimensionMatch map[string]string `yaml:"dimension_match"`
			} `yaml:"pricing_rules"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		models := map[string]bool{}
		for _, r := range doc.PricingRules {
			if m := r.DimensionMatch["model"]; m != "" {
				models[m] = true
			}
		}
		return models
	}

	anthropic := recipeModels("../../recipe/recipes/anthropic_style.yaml")
	openai := recipeModels("../../recipe/recipes/openai_style.yaml")

	for _, f := range modelFamilies {
		var priced bool
		var where string
		switch {
		case strings.HasPrefix(f.recipeToken, "claude-"):
			priced, where = anthropic[f.recipeToken], "anthropic_style.yaml"
		case strings.HasPrefix(f.recipeToken, "gpt-"), strings.HasPrefix(f.recipeToken, "text-embedding-"):
			priced, where = openai[f.recipeToken], "openai_style.yaml"
		default:
			t.Errorf("model family %q has no recipe mapping rule in this test — extend the switch when adding a provider", f.recipeToken)
			continue
		}
		if !priced {
			t.Errorf("mapper emits model=%q (prefix %q) but %s has NO pricing rule for it — that traffic ingests and then silently doesn't bill at cycle close", f.recipeToken, f.prefix, where)
		}
	}
}

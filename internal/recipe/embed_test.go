package recipe

import (
	"testing"
)

// TestLoad asserts that all bundled recipes parse and validate cleanly at
// process start. A regression here is a boot-time fatal in production —
// catching it in CI keeps the binary buildable.
func TestLoad(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	keys := reg.Keys()
	if len(keys) == 0 {
		t.Fatal("registry is empty: expected at least one bundled recipe")
	}
	for _, k := range keys {
		t.Logf("loaded recipe: %s", k)
	}
}

// TestRegistryGet exercises the lookup-or-miss contract: known key returns
// (recipe, true); unknown returns (zero, false). Service.Preview/Instantiate
// rely on the bool to issue 404 cleanly.
func TestRegistryGet(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if _, ok := reg.Get("anthropic_style"); !ok {
		t.Error("expected anthropic_style to be loaded")
	}
	if _, ok := reg.Get("nonsense_key"); ok {
		t.Error("expected unknown key to miss")
	}
}

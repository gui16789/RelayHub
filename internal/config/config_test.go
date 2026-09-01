package config

// AllModels must stay consistent with routing: it only lists models
// declared by ENABLED channels, so /v1/models never advertises a model a
// live request would 404 on (see P2: model-list/route consistency).

import (
	"reflect"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestAllModelsOnlyEnabledChannels(t *testing.T) {
	cfg := &Config{
		Channels: []Channel{
			{Name: "a", Models: []string{"shared", "alpha"}},
			{Name: "b", Enabled: boolPtr(false), Models: []string{"shared", "hidden"}},
			{Name: "c", Models: []string{"shared", "wild-*"}},
		},
	}
	got := cfg.AllModels()
	want := []string{"alpha", "shared"} // "hidden" (disabled channel) and wildcards are out
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllModels() = %v, want %v", got, want)
	}
}

func TestAllModelsNilEnabledMeansEnabled(t *testing.T) {
	cfg := &Config{Channels: []Channel{{Name: "a", Models: []string{"m"}}}}
	if got := cfg.AllModels(); !reflect.DeepEqual(got, []string{"m"}) {
		t.Errorf("AllModels() = %v, want [m] (nil enabled flag = enabled)", got)
	}
}

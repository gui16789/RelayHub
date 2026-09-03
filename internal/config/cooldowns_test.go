package config

import (
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration must round-trip a human-friendly string through both YAML and
// JSON, because config.yaml is hand-edited and the admin API speaks JSON.

func TestDurationYAMLRoundTrip(t *testing.T) {
	raw := []byte("cooldowns:\n  rate_limit: 5s\n  quota_max: 2h30m\n")
	var holder struct {
		Cooldowns Cooldowns `yaml:"cooldowns"`
	}
	if err := yaml.Unmarshal(raw, &holder); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := holder.Cooldowns.RateLimit.D(); got != 5*time.Second {
		t.Errorf("rate_limit = %v, want 5s", got)
	}
	if got := holder.Cooldowns.QuotaMax.D(); got != 150*time.Minute {
		t.Errorf("quota_max = %v, want 2h30m", got)
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"rate_limit":"5s"}`)
	var cd Cooldowns
	if err := json.Unmarshal(raw, &cd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := cd.RateLimit.D(); got != 5*time.Second {
		t.Errorf("rate_limit = %v, want 5s", got)
	}
	out, err := json.Marshal(Cooldowns{RateLimit: Duration(90 * time.Second)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `{"rate_limit":"1m30s"}` {
		t.Errorf("marshal = %s, want duration string", out)
	}
}

func TestDurationRejectsBadValues(t *testing.T) {
	for _, bad := range []string{"rate_limit: soon", "rate_limit: -5s", "rate_limit: 5"} {
		var holder struct {
			Cooldowns Cooldowns `yaml:"cooldowns"`
		}
		if err := yaml.Unmarshal([]byte("cooldowns:\n  "+bad+"\n"), &holder); err == nil {
			t.Errorf("%s: want error, got nil", bad)
		}
	}
}

// EffectiveCooldowns must resolve the three-level inheritance chain:
// channel > server > built-in. Zero fields inherit; explicit values win.

func TestEffectiveCooldownsInheritance(t *testing.T) {
	server := Cooldowns{RateLimit: Duration(30 * time.Second), QuotaBase: Duration(time.Minute)}
	channel := Cooldowns{RateLimit: Duration(5 * time.Second)} // overrides rate only

	got := EffectiveCooldowns(server, channel)
	if got.RateLimit.D() != 5*time.Second {
		t.Errorf("rate_limit = %v, want 5s (channel wins)", got.RateLimit.D())
	}
	if got.QuotaBase.D() != time.Minute {
		t.Errorf("quota_base = %v, want 1m (server wins)", got.QuotaBase.D())
	}
	if got.QuotaMax.D() != BuiltinCooldowns().QuotaMax.D() {
		t.Errorf("quota_max = %v, want built-in %v", got.QuotaMax.D(), BuiltinCooldowns().QuotaMax.D())
	}
	if got.Auth.D() != BuiltinCooldowns().Auth.D() {
		t.Errorf("auth = %v, want built-in %v", got.Auth.D(), BuiltinCooldowns().Auth.D())
	}
}

func TestEffectiveCooldownsEmptyGivesBuiltins(t *testing.T) {
	got := EffectiveCooldowns(Cooldowns{}, Cooldowns{})
	want := BuiltinCooldowns()
	if got != want {
		t.Errorf("EffectiveCooldowns(zero, zero) = %+v, want built-ins %+v", got, want)
	}
}

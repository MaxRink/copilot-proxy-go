package handler

import (
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

func modelWithContext(id string, ctx int) state.Model {
	m := state.Model{ID: id}
	m.Capabilities.Limits.MaxContextWindowTokens = ctx
	m.Capabilities.Limits.MaxPromptTokens = ctx
	return m
}

func tieredModel(id string, def, long int) state.Model {
	m := modelWithContext(id, long)
	m.Billing.TokenPrices = map[string]state.PriceTier{
		"default":      {MaxPromptTokens: def},
		"long_context": {MaxPromptTokens: long},
	}
	return m
}

func TestModelFlavours(t *testing.T) {
	tests := []struct {
		name string
		in   state.Model
		want []ModelFlavour
	}{
		{"tiered claude gets default and [1m]", tieredModel("claude-haiku-5.5", 100000, 936000), []ModelFlavour{
			{ID: "claude-haiku-5-5", ContextWindow: 100000},
			{ID: "claude-haiku-5-5[1m]", ContextWindow: 936000, Long: true},
		}},
		{"tiered non-claude gets [long]", tieredModel("gpt-6-luna", 272000, 872000), []ModelFlavour{
			{ID: "gpt-6-luna", ContextWindow: 272000},
			{ID: "gpt-6-luna[long]", ContextWindow: 872000, Long: true},
		}},
		{"single-tier claude over 200K still gets [1m]", modelWithContext("claude-opus-4.8", 1000000), []ModelFlavour{
			{ID: "claude-opus-4-8[1m]", ContextWindow: 1000000, Long: true},
		}},
		{"single-tier claude at 200K stays plain", modelWithContext("claude-sonnet-4.5", 200000), []ModelFlavour{
			{ID: "claude-sonnet-4-5", ContextWindow: 200000},
		}},
		{"reasoning suffix preserved", modelWithContext("claude-opus-4.7-high", 200000), []ModelFlavour{
			{ID: "claude-opus-4-7-high", ContextWindow: 200000},
		}},
		{"future claude family is dashed", modelWithContext("claude-fable-5.1", 200000), []ModelFlavour{
			{ID: "claude-fable-5-1", ContextWindow: 200000},
		}},
		{"single-tier non-claude passes through", modelWithContext("gpt-5.3-codex", 272000), []ModelFlavour{
			{ID: "gpt-5.3-codex", ContextWindow: 272000},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ModelFlavours(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

func TestResolveCopilotModel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-4-8[1m]", "claude-opus-4.8"}, // defensive strip
		{"gpt-6-luna[long]", "gpt-6-luna"},
		{"claude-sonnet-4", "claude-sonnet-4"},
		{"claude-opus-4-7-high", "claude-opus-4.7-high"},
		{"gpt-5.5", "gpt-5.5"}, // non-claude unchanged
	}
	for _, tt := range tests {
		if got := ResolveCopilotModel(tt.in); got != tt.want {
			t.Fatalf("ResolveCopilotModel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

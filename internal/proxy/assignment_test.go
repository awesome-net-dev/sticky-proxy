package proxy

import (
	"testing"
)

func TestEffectiveWeight_DefaultsToOne(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		weight int
		want   int
	}{
		{"zero", 0, 1},
		{"negative", -5, 1},
		{"one", 1, 1},
		{"positive", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &Assignment{Weight: tt.weight}
			if got := a.EffectiveWeight(); got != tt.want {
				t.Errorf("EffectiveWeight() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUnmarshalAssignment_WithWeight(t *testing.T) {
	t.Parallel()

	data := `{"backend":"http://b1:8080","assigned_at":"2024-01-01T00:00:00Z","source":"assignment","weight":5}`
	a, err := unmarshalAssignment(data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Backend != "http://b1:8080" {
		t.Errorf("Backend = %q, want %q", a.Backend, "http://b1:8080")
	}
	if a.Weight != 5 {
		t.Errorf("Weight = %d, want 5", a.Weight)
	}
	if a.EffectiveWeight() != 5 {
		t.Errorf("EffectiveWeight() = %d, want 5", a.EffectiveWeight())
	}
}

func TestUnmarshalAssignment_WithoutWeight(t *testing.T) {
	t.Parallel()

	data := `{"backend":"http://b1:8080","assigned_at":"2024-01-01T00:00:00Z","source":"hash"}`
	a, err := unmarshalAssignment(data)
	if err != nil {
		t.Fatal(err)
	}
	if a.Weight != 0 {
		t.Errorf("Weight = %d, want 0", a.Weight)
	}
	if a.EffectiveWeight() != 1 {
		t.Errorf("EffectiveWeight() = %d, want 1 (default)", a.EffectiveWeight())
	}
}

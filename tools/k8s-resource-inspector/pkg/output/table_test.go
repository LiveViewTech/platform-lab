package output

import (
	"testing"

	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/analysis"
	"github.com/LiveViewTech/platform-lab/tools/k8s-resource-inspector/pkg/hpa"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestRecFlag(t *testing.T) {
	tests := []struct {
		name string
		rec  analysis.Recommendation
		want string
	}{
		{
			name: "hold",
			rec:  analysis.Recommendation{Hold: true, HoldReason: "SPIKY"},
			want: "hold",
		},
		{
			name: "no text → dash",
			rec:  analysis.Recommendation{},
			want: "-",
		},
		{
			name: "within tolerance (not actionable)",
			rec:  analysis.Recommendation{Text: "within tolerance", IsActionable: false},
			want: "ok",
		},
		{
			name: "actionable",
			rec:  analysis.Recommendation{Text: "CPU 100m -> 10m", IsActionable: true},
			want: "YES",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recFlag(tt.rec)
			if got != tt.want {
				t.Errorf("recFlag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHpaFlag(t *testing.T) {
	tests := []struct {
		name string
		v    hpa.Validation
		want string
	}{
		{"none", hpa.Validation{Status: "NONE"}, "-"},
		{"empty", hpa.Validation{}, "-"},
		{"ok", hpa.Validation{Status: "OK"}, "OK"},
		{"warn", hpa.Validation{Status: "WARN"}, "WARN"},
		{"error", hpa.Validation{Status: "ERROR"}, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hpaFlag(tt.v)
			if got != tt.want {
				t.Errorf("hpaFlag() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMemLimRatio(t *testing.T) {
	lim128 := resource.MustParse("128Mi")
	zero := resource.Quantity{}

	tests := []struct {
		name   string
		memP99 float64
		lim    resource.Quantity
		want   string
	}{
		{"zero p99", 0, lim128, "-"},
		{"zero limit", 64 * 1048576, zero, "-"},
		{"50%", 64 * 1048576, lim128, "50%"},
		{"90%", float64(128*1048576) * 0.9, lim128, "90%"},
		{"100%", float64(128 * 1048576), lim128, "100%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := memLimRatio(tt.memP99, tt.lim)
			if got != tt.want {
				t.Errorf("memLimRatio() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfOrDash(t *testing.T) {
	tests := []struct {
		c    float64
		want string
	}{
		{0, "-"},
		{0.9, "90%"},
		{0.95, "95%"},
		{1.0, "100%"},
	}
	for _, tt := range tests {
		got := confOrDash(tt.c)
		if got != tt.want {
			t.Errorf("confOrDash(%v) = %q, want %q", tt.c, got, tt.want)
		}
	}
}

func TestCoresOrDash(t *testing.T) {
	tests := []struct {
		cores   float64
		hasData bool
		want    string
	}{
		{0, false, "-"},
		{0.1, false, "-"},
		{0, true, "0"},
		{0.1, true, "100m"},
		{0.01, true, "10m"},
	}
	for _, tt := range tests {
		got := coresOrDash(tt.cores, tt.hasData)
		if got != tt.want {
			t.Errorf("coresOrDash(%v, %v) = %q, want %q", tt.cores, tt.hasData, got, tt.want)
		}
	}
}

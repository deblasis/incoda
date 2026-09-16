package tui

import (
	"strings"
	"testing"

	"github.com/deblasis/incoda/internal/sysinfo"
)

func TestRenderGaugeBarWithAvailableMemory(t *testing.T) {
	m := newTestModel(nil)
	m.rep.Memory = sysinfo.Memory{
		HaveTotal: true, TotalBytes: 24 << 30,
		HaveAvailable: true, AvailableBytes: 6 << 30,
		Source: "test",
	}
	m.rep.CPU = sysinfo.CPU{HaveUsage: true, UsagePct: 42, Source: "test"}

	out := m.renderGauge(120)
	if strings.Contains(out, "free unavailable") {
		t.Fatalf("gauge fell back to plain text: %q", out)
	}
	if !strings.Contains(out, "█") {
		t.Fatalf("expected memory bar, got %q", out)
	}
	if !strings.Contains(out, "42%") {
		t.Fatalf("expected cpu percentage, got %q", out)
	}
}

func TestRenderGaugeFallbackWithoutAvailableMemory(t *testing.T) {
	m := newTestModel(nil)
	m.rep.Memory = sysinfo.Memory{
		HaveTotal: true, TotalBytes: 24 << 30,
		Source: "sysctl-only",
	}

	out := m.renderGauge(120)
	if strings.Contains(out, "█") {
		t.Fatalf("expected plain fallback without bar, got %q", out)
	}
	if !strings.Contains(out, "24.0 GB total") {
		t.Fatalf("expected sysctl fallback text, got %q", out)
	}
}

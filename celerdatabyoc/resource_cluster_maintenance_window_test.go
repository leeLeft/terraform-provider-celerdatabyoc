package celerdatabyoc

import (
	"strings"
	"testing"

	"terraform-provider-celerdatabyoc/celerdata-sdk/service/cluster"
)

func TestParseMaintenanceWindowTime(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"00:00", 0, false},
		{"02:30", 150, false},
		{"23:59", 1439, false},
		{"9:00", 0, true}, // must be zero-padded, backend reads back "09:00"
		{"24:00", 0, true},
		{"02:60", 0, true},
		{"0200", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseMaintenanceWindowTime(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseMaintenanceWindowTime(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseMaintenanceWindowTime(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestValidateMaintenanceWindowConfig(t *testing.T) {
	base := maintenanceWindowConfig{
		WindowType: "WEEKLY", DayOfWeek: "SUNDAY",
		StartTime: "02:00", EndTime: "05:00",
	}
	with := func(mut func(*maintenanceWindowConfig)) maintenanceWindowConfig {
		c := base
		mut(&c)
		return c
	}

	tests := []struct {
		name        string
		cfg         maintenanceWindowConfig
		errContains string
	}{
		{"valid weekly", base, ""},
		{"valid monthly", with(func(c *maintenanceWindowConfig) {
			c.WindowType, c.DayOfWeek, c.DayOfMonth = "MONTHLY", "", 15
		}), ""},
		{"exactly 120 minutes is ok", with(func(c *maintenanceWindowConfig) { c.EndTime = "04:00" }), ""},
		{"119 minutes rejected", with(func(c *maintenanceWindowConfig) { c.EndTime = "03:59" }), "at least 120 minutes"},
		{"end before start rejected", with(func(c *maintenanceWindowConfig) { c.EndTime = "01:00" }), "at least 120 minutes"},
		{"weekly needs day_of_week", with(func(c *maintenanceWindowConfig) { c.DayOfWeek = "" }), "day_of_week"},
		{"weekly rejects day_of_month", with(func(c *maintenanceWindowConfig) { c.DayOfMonth = 3 }), "day_of_month"},
		{"monthly needs day_of_month", with(func(c *maintenanceWindowConfig) {
			c.WindowType, c.DayOfWeek = "MONTHLY", ""
		}), "day_of_month"},
		{"monthly rejects day_of_week", with(func(c *maintenanceWindowConfig) {
			c.WindowType, c.DayOfMonth = "MONTHLY", 15
		}), "day_of_week"},
		{"bad start_time", with(func(c *maintenanceWindowConfig) { c.StartTime = "2:00" }), "start_time"},
		{"bad end_time", with(func(c *maintenanceWindowConfig) { c.EndTime = "5pm" }), "end_time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaintenanceWindowConfig(tt.cfg)
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errContains)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
			}
		})
	}
}

func TestCheckMaintenanceWindowTypeConsistent(t *testing.T) {
	weekly := &cluster.MaintenanceWindow{WindowId: "w-1", WindowType: "WEEKLY"}
	monthly := &cluster.MaintenanceWindow{WindowId: "w-2", WindowType: "MONTHLY"}

	tests := []struct {
		name        string
		existing    []*cluster.MaintenanceWindow
		selfID      string
		windowType  string
		errContains string
	}{
		{"no existing windows", nil, "", "MONTHLY", ""},
		{"same type as siblings", []*cluster.MaintenanceWindow{weekly}, "", "WEEKLY", ""},
		{"different type rejected", []*cluster.MaintenanceWindow{weekly}, "", "MONTHLY", `existing window w-1 of type "WEEKLY"`},
		{"only window may switch type", []*cluster.MaintenanceWindow{weekly}, "w-1", "MONTHLY", ""},
		{"switch rejected while a sibling remains", []*cluster.MaintenanceWindow{weekly, monthly}, "w-2", "WEEKLY", ""},
		{"switch rejected by sibling of old type", []*cluster.MaintenanceWindow{weekly, {WindowId: "w-3", WindowType: "WEEKLY"}}, "w-1", "MONTHLY", "w-3"},
		{"nil entries ignored", []*cluster.MaintenanceWindow{nil, weekly}, "", "WEEKLY", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMaintenanceWindowTypeConsistent(tt.existing, tt.selfID, tt.windowType)
			if tt.errContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errContains)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
			}
		})
	}
}

func TestMaintenanceWindowIDRoundTrip(t *testing.T) {
	id := encodeMaintenanceWindowID("c-1", "w-1")
	if id != "c-1:w-1" {
		t.Fatalf("encode = %q", id)
	}
	c, w, err := decodeMaintenanceWindowID(id)
	if err != nil || c != "c-1" || w != "w-1" {
		t.Fatalf("decode = (%q, %q, %v)", c, w, err)
	}
	for _, bad := range []string{"", "c-1", ":w-1", "c-1:"} {
		if _, _, err := decodeMaintenanceWindowID(bad); err == nil {
			t.Errorf("decode(%q) expected error", bad)
		}
	}
	// A window id may itself contain ':' — only the first separator splits.
	c, w, err = decodeMaintenanceWindowID("c-1:w:1")
	if err != nil || c != "c-1" || w != "w:1" {
		t.Fatalf("decode with embedded sep = (%q, %q, %v)", c, w, err)
	}
}

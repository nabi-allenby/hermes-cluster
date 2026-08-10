package session

import "testing"

func TestDerive(t *testing.T) {
	tests := []struct {
		name string
		f    Facts
		want State
	}{
		{"no sandbox yet", Facts{}, StatePending},
		{"terminating wins over everything", Facts{Terminating: true, HasSandbox: true, OperatingMode: "Running", Ready: true}, StateTerminating},
		{"running ready", Facts{HasSandbox: true, OperatingMode: "Running", Ready: true}, StateReady},
		{"running not ready is waking", Facts{HasSandbox: true, OperatingMode: "Running"}, StateWaking},
		{"empty mode defaults to running semantics, ready", Facts{HasSandbox: true, Ready: true}, StateReady},
		{"empty mode not ready is waking", Facts{HasSandbox: true}, StateWaking},
		{"suspended confirmed", Facts{HasSandbox: true, OperatingMode: "Suspended", Suspended: true}, StateSuspended},
		{"suspended pending pod teardown", Facts{HasSandbox: true, OperatingMode: "Suspended"}, StateSuspending},
		// A suspended sandbox can briefly still report Ready=True while the
		// controller catches up; operatingMode wins.
		{"suspended overrides stale ready", Facts{HasSandbox: true, OperatingMode: "Suspended", Ready: true}, StateSuspending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Derive(tt.f); got != tt.want {
				t.Fatalf("Derive(%+v) = %q, want %q", tt.f, got, tt.want)
			}
		})
	}
}

func TestValidateID(t *testing.T) {
	for _, ok := range []string{"s-abc123", "a", "session-1", "x9"} {
		if err := ValidateID(ok); err != nil {
			t.Errorf("ValidateID(%q) unexpectedly failed: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-abc", "abc-", "UPPER", "has_underscore", "a.b", "waytoolong" + string(make([]byte, 60))} {
		if err := ValidateID(bad); err == nil {
			t.Errorf("ValidateID(%q) unexpectedly passed", bad)
		}
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if err := ValidateID(id); err != nil {
			t.Fatalf("NewID() produced invalid id: %v", err)
		}
		if seen[id] {
			t.Fatalf("NewID() produced duplicate %q", id)
		}
		seen[id] = true
	}
}

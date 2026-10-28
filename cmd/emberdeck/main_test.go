package main

import (
	"strings"
	"testing"
)

func TestLocalDriverLogsNameTheRealTransport(t *testing.T) {
	for _, tc := range []struct {
		mode, tcp, serial, want string
	}{
		{"simulate", "", "", "simulated driver"},
		{"tcp", "192.0.2.40:23", "", "TCP driver 192.0.2.40:23"},
		{"serial", "", "/dev/ttyUSB0", "serial driver /dev/ttyUSB0"},
	} {
		if got := localDriverLabel(tc.mode, tc.tcp, tc.serial); got != tc.want {
			t.Errorf("%s label = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestServeMetricsListenFlagIsOptionalAndRecognized(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "")
	if err := runServe([]string{"-metrics-listen", "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "EMBERDECK_API_KEY is required") {
		t.Fatalf("metrics listen flag was not accepted: %v", err)
	}
	if err := runServe(nil); err == nil || !strings.Contains(err.Error(), "EMBERDECK_API_KEY is required") {
		t.Fatalf("default serve behavior changed: %v", err)
	}
}

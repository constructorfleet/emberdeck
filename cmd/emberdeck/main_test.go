package main

import "testing"

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

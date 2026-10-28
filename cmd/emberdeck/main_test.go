package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/transport"
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

func TestSerialModemFlagsDefaultOffAndApplyRequestedBits(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		flags := flag.NewFlagSet("driver", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		dtr, rts := addSerialModemFlags(flags)
		if err := flags.Parse(nil); err != nil {
			t.Fatal(err)
		}
		got, err := parseSerialModemBits(*dtr, *rts)
		if err != nil {
			t.Fatal(err)
		}
		if got != (transport.ModemBits{}) {
			t.Fatalf("default modem bits = %+v; want DTR/RTS off", got)
		}
	})

	t.Run("configured", func(t *testing.T) {
		flags := flag.NewFlagSet("driver", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		dtr, rts := addSerialModemFlags(flags)
		if err := flags.Parse([]string{"-serial-dtr=on", "-serial-rts=off"}); err != nil {
			t.Fatal(err)
		}
		got, err := parseSerialModemBits(*dtr, *rts)
		if err != nil {
			t.Fatal(err)
		}
		want := transport.ModemBits{DTR: true, RTS: false}
		if got != want {
			t.Fatalf("configured modem bits = %+v; want %+v", got, want)
		}
	})
}

func TestParseSerialModemBitsRejectsUnknownValues(t *testing.T) {
	if _, err := parseSerialModemBits("pulse", "off"); err == nil {
		t.Fatal("invalid DTR policy was accepted")
	}
	if _, err := parseSerialModemBits("off", "pulse"); err == nil {
		t.Fatal("invalid RTS policy was accepted")
	}
}

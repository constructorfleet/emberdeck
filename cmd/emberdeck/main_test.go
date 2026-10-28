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

func TestLoggerHonorsLevelAndFormat(t *testing.T) {
	var out strings.Builder
	logger, err := newLogger(&out, "warn", "json")
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hidden")
	logger.Warn("shown", "job_id", "abc")
	if got := out.String(); strings.Contains(got, "hidden") || !strings.Contains(got, `"msg":"shown"`) || !strings.Contains(got, `"job_id":"abc"`) {
		t.Fatalf("warn/json logger wrote %q", got)
	}
	out.Reset()
	logger, err = newLogger(&out, "", "")
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("debug hidden by default")
	logger.Info("info shown by default")
	if got := out.String(); strings.Contains(got, "debug hidden") || !strings.Contains(got, "level=INFO msg=\"info shown by default\"") {
		t.Fatalf("default logger wrote %q", got)
	}
	for _, bad := range [][2]string{{"verbose", "text"}, {"info", "xml"}} {
		if _, err := newLogger(&out, bad[0], bad[1]); err == nil {
			t.Errorf("newLogger(%q, %q) accepted an invalid setting", bad[0], bad[1])
		}
	}
}

func TestLogFlagsFallBackToEnvironment(t *testing.T) {
	t.Setenv("EMBERDECK_LOG_LEVEL", "debug")
	t.Setenv("EMBERDECK_LOG_FORMAT", "json")
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	level, format := addLogFlags(flags)
	if err := flags.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if *level != "debug" || *format != "json" {
		t.Fatalf("environment defaults = %q/%q", *level, *format)
	}
	if err := flags.Parse([]string{"-log-level", "error", "-log-format", "text"}); err != nil {
		t.Fatal(err)
	}
	if *level != "error" || *format != "text" {
		t.Fatalf("flags did not override environment: %q/%q", *level, *format)
	}
}

func TestServeRejectsInvalidLogLevelBeforeOpeningLibrary(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "key")
	if err := runServe([]string{"-log-level", "loud", "-data", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "-log-level") {
		t.Fatalf("invalid log level accepted: %v", err)
	}
}

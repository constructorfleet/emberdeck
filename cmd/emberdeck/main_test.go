package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
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

func TestServeReadsAPIKeyFromFileEnvironmentVariable(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(keyFile, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBERDECK_API_KEY", "")
	t.Setenv("EMBERDECK_API_KEY_FILE", keyFile)
	err := runServe([]string{"-listen", "not-an-address", "-data", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "listen:") {
		t.Fatalf("serve did not load API key from file before attempting listen: %v", err)
	}
}

func TestLogFlagsReadDockerFileEnvironmentVariables(t *testing.T) {
	levelFile := filepath.Join(t.TempDir(), "log-level")
	if err := os.WriteFile(levelFile, []byte("debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBERDECK_LOG_LEVEL", "")
	t.Setenv("EMBERDECK_LOG_LEVEL_FILE", levelFile)
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	level, _, err := addLogFlags(flags)
	if err != nil {
		t.Fatal(err)
	}
	if err := flags.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if *level != "debug" {
		t.Fatalf("log level from file = %q, want debug", *level)
	}
}

func TestDirectEnvironmentValueTakesPrecedenceOverFile(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "direct-key")
	t.Setenv("EMBERDECK_API_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	err := runServe([]string{"-listen", "not-an-address", "-data", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "listen:") {
		t.Fatalf("direct API key did not take precedence over missing file: %v", err)
	}
}

func TestServeReportsUnreadableSecretFile(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "")
	t.Setenv("EMBERDECK_API_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	err := runServe(nil)
	if err == nil || !strings.Contains(err.Error(), "read EMBERDECK_API_KEY_FILE") {
		t.Fatalf("unreadable API key file error = %v", err)
	}
}

func TestServeMetricsListenFlagIsOptionalAndRecognized(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "")
	if err := runServe([]string{"-listen", "not-an-address", "-data", t.TempDir(), "-metrics-listen", "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), "listen:") {
		t.Fatalf("metrics listen flag was not accepted: %v", err)
	}
}

func TestServeDoesNotRequireAPIKey(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "")
	if err := runServe([]string{"-listen", "not-an-address", "-data", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "listen:") {
		t.Fatalf("keyless serve did not reach the listener: %v", err)
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
	level, format, err := addLogFlags(flags)
	if err != nil {
		t.Fatal(err)
	}
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

func TestServeJobEventRetentionFlagsAreValidated(t *testing.T) {
	t.Setenv("EMBERDECK_API_KEY", "")
	if err := runServe([]string{"-listen", "not-an-address", "-data", t.TempDir(), "-job-event-limit", "1000", "-job-event-retention", "720h"}); err == nil || !strings.Contains(err.Error(), "listen:") {
		t.Fatalf("job event retention flags were not accepted: %v", err)
	}
	for _, args := range [][]string{{"-job-event-limit", "0"}, {"-job-event-limit", "-1"}, {"-job-event-retention", "-1h"}} {
		if err := runServe(args); err == nil || !strings.Contains(err.Error(), "job-event") {
			t.Fatalf("%v: invalid retention accepted: %v", args, err)
		}
	}
}

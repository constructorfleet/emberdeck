package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Settings holds the GRBL laser and travel settings read with the read-only
// `$$` command. A nil field was not reported (or, in an expectation, is not
// compared). Boolean settings ($20, $32) are 0 or 1.
type Settings struct {
	SoftLimits *float64 `json:"$20,omitempty"`
	MaxSpindle *float64 `json:"$30,omitempty"`
	MinSpindle *float64 `json:"$31,omitempty"`
	LaserMode  *float64 `json:"$32,omitempty"`
	MaxTravelX *float64 `json:"$130,omitempty"`
	MaxTravelY *float64 `json:"$131,omitempty"`
}

type settingField struct {
	number int
	label  string
	get    func(*Settings) **float64
}

var settingFields = []settingField{
	{20, "soft limits", func(s *Settings) **float64 { return &s.SoftLimits }},
	{30, "maximum spindle", func(s *Settings) **float64 { return &s.MaxSpindle }},
	{31, "minimum spindle", func(s *Settings) **float64 { return &s.MinSpindle }},
	{32, "laser mode", func(s *Settings) **float64 { return &s.LaserMode }},
	{130, "X max travel", func(s *Settings) **float64 { return &s.MaxTravelX }},
	{131, "Y max travel", func(s *Settings) **float64 { return &s.MaxTravelY }},
}

// Valid reports whether every present value is finite and the boolean
// settings are 0 or 1.
func (s Settings) Valid() bool {
	for _, field := range settingFields {
		v := *field.get(&s)
		if v == nil {
			continue
		}
		if math.IsNaN(*v) || math.IsInf(*v, 0) || math.Abs(*v) > 1e12 {
			return false
		}
		if (field.number == 20 || field.number == 32) && *v != 0 && *v != 1 {
			return false
		}
	}
	return true
}

// Mismatches compares the reported settings with each non-nil expected value
// and returns one description per differing or unreported setting, for
// example "$30 (maximum spindle): controller 255, profile 1000".
func (s Settings) Mismatches(expected Settings) []string {
	var diff []string
	for _, field := range settingFields {
		want := *field.get(&expected)
		if want == nil {
			continue
		}
		got := *field.get(&s)
		name := fmt.Sprintf("$%d (%s)", field.number, field.label)
		switch {
		case got == nil:
			diff = append(diff, fmt.Sprintf("%s: not reported by controller, profile %s", name, formatSetting(*want)))
		case math.Abs(*got-*want) > 1e-9:
			diff = append(diff, fmt.Sprintf("%s: controller %s, profile %s", name, formatSetting(*got), formatSetting(*want)))
		}
	}
	return diff
}

func formatSetting(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// settingsParser accumulates `$N=value` lines from a `$$` reply.
type settingsParser struct{ settings Settings }

// line consumes one controller line. It reports whether the line was a
// setting and fails on a malformed value of a setting EmberDeck reads.
func (p *settingsParser) line(line string) (bool, error) {
	if !strings.HasPrefix(line, "$") {
		return false, nil
	}
	key, raw, ok := strings.Cut(line[1:], "=")
	if !ok {
		return false, nil
	}
	number, err := strconv.Atoi(key)
	if err != nil {
		return true, nil
	}
	for _, field := range settingFields {
		if field.number != number {
			continue
		}
		// GRBL 0.9 appended a parenthetical description; 1.1 prints the value only.
		text := strings.Fields(raw)
		if len(text) == 0 {
			return true, fmt.Errorf("driver: controller setting $%d has no value", number)
		}
		v, err := strconv.ParseFloat(text[0], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return true, fmt.Errorf("driver: controller setting $%d has unreadable value %q", number, text[0])
		}
		*field.get(&p.settings) = &v
	}
	return true, nil
}

// ReadSettings sends the read-only `$$` query and parses its reply. It never
// writes settings or program bytes. A startup banner, alarm, or error reply
// fails closed. It does not close conn.
func ReadSettings(ctx context.Context, conn io.ReadWriteCloser, timeout time.Duration) (Settings, error) {
	if ctx == nil || conn == nil {
		return Settings{}, fmt.Errorf("driver: context and connection are required")
	}
	if timeout <= 0 {
		timeout = responseTimeout
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, "$$\n"); err != nil {
		return Settings{}, fmt.Errorf("driver: request controller settings: %w", err)
	}
	reader := bufio.NewReader(conn)
	deadline := time.Now().Add(timeout)
	var parser settingsParser
	for {
		line, err := readLineUntil(ctx, conn, reader, deadline)
		if err != nil {
			return Settings{}, fmt.Errorf("driver: read controller settings: %w", err)
		}
		if err := settingsReplyError(line); err != nil {
			return Settings{}, err
		}
		if line == "ok" {
			return parser.settings, nil
		}
		if _, err := parser.line(line); err != nil {
			return Settings{}, err
		}
	}
}

func settingsReplyError(line string) error {
	switch {
	case strings.HasPrefix(line, "Grbl "):
		return fmt.Errorf("driver: controller reset during settings read (%s); work coordinates must be re-verified before starting a job", line)
	case strings.HasPrefix(line, "ALARM:"):
		return fmt.Errorf("driver: controller alarm during settings read: %s", line)
	case strings.HasPrefix(line, "error:"):
		return fmt.Errorf("driver: controller rejected settings read: %s", line)
	}
	return nil
}

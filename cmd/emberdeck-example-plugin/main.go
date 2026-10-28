// emberdeck-example-plugin is a small v1 G-code preflight plugin.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type request struct {
	ProtocolVersion int    `json:"protocolVersion"`
	GCode           string `json:"gcode"`
}
type response struct {
	ProtocolVersion int    `json:"protocolVersion"`
	OK              bool   `json:"ok"`
	Message         string `json:"message,omitempty"`
}

func main() {
	dec := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20+1))
	dec.DisallowUnknownFields()
	var req request
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(response{ProtocolVersion: 1, Message: "invalid request"})
		return
	}
	if req.ProtocolVersion != 1 {
		emit(false, "unsupported protocol version")
		return
	}
	if strings.TrimSpace(req.GCode) == "" {
		emit(false, "G-code is empty")
		return
	}
	scanner := bufio.NewScanner(strings.NewReader(req.GCode))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(strings.SplitN(scanner.Text(), ";", 2)[0])
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		if strings.EqualFold(fields[0], "M0") || strings.EqualFold(fields[0], "M1") || strings.EqualFold(fields[0], "M2") || strings.EqualFold(fields[0], "M30") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(fields[0]), "G") || strings.HasPrefix(strings.ToUpper(fields[0]), "M") || strings.HasPrefix(strings.ToUpper(fields[0]), "T") {
			continue
		}
		emit(false, fmt.Sprintf("line %d: unrecognized command", line))
		return
	}
	if err := scanner.Err(); err != nil {
		emit(false, "G-code line is too long")
		return
	}
	emit(true, "G-code passed basic syntax preflight")
}

func emit(ok bool, message string) {
	_ = json.NewEncoder(os.Stdout).Encode(response{ProtocolVersion: 1, OK: ok, Message: message})
}

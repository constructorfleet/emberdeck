package driver

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestReadStatusQueriesAndReturnsControllerState(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan string, 1)
	go func() {
		var query [1]byte
		_, _ = machine.Read(query[:])
		wire <- string(query[:])
		_, _ = io.WriteString(machine, "ok\r\n<Idle|MPos:1,2,0>\r\n")
	}()
	status, err := ReadStatus(context.Background(), client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-wire; got != "?" {
		t.Fatalf("query = %q; want ?", got)
	}
	if status.State != "Idle" || status.MachinePosition == nil || status.MachinePosition.X != 1 || status.MachinePosition.Y != 2 {
		t.Fatalf("status = %#v", status)
	}
}

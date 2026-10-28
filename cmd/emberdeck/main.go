// EmberDeck manages laser cutter jobs and local or remote machine drivers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"github.com/ConstructorFleet/emberdeck/transport"
	"github.com/ConstructorFleet/emberdeck/worker"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "-version" || args[0] == "--version" || args[0] == "version") {
		fmt.Println("EmberDeck", version)
		return nil
	}
	if len(args) == 0 || (args[0] != "serve" && args[0] != "driver") {
		return errors.New("usage: emberdeck serve [-listen address] [-data directory] [-plugins directory] [-simulate | -tcp address | -serial port [-baud rate]] | emberdeck driver -server URL [-id name] (-simulate | -tcp address | -serial port [-baud rate]) | emberdeck driver -list-ports | emberdeck -version")
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "driver":
		return runDriver(args[1:])
	default:
		return errors.New("usage: emberdeck serve [-listen address] [-data directory] [-plugins directory] [-simulate | -tcp address | -serial port [-baud rate]] | emberdeck driver -server URL [-id name] (-simulate | -tcp address | -serial port [-baud rate]) | emberdeck driver -list-ports | emberdeck -version")
	}
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listenAddress := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	data := flags.String("data", "data", "job library directory")
	plugins := flags.String("plugins", "", "optional directory of plugins to load")
	simulate := flags.Bool("simulate", false, "run a simulated local driver")
	tcpAddress := flags.String("tcp", "", "run a driver connected to a GRBL TCP controller")
	serialPort := flags.String("serial", "", "run a driver connected to a serial controller")
	baud := flags.Int("baud", 115200, "serial connection baud rate")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}
	if connectionCount(*simulate, *tcpAddress, *serialPort) > 1 {
		return errors.New("choose at most one local driver connection: -simulate, -tcp address, or -serial port")
	}
	if *serialPort != "" && *baud <= 0 {
		return errors.New("-baud must be positive")
	}
	key := os.Getenv("EMBERDECK_API_KEY")
	if key == "" {
		return errors.New("EMBERDECK_API_KEY is required")
	}
	handler, err := server.OpenWithPlugins(*data, key, *plugins)
	if err != nil {
		return fmt.Errorf("open job library: %w", err)
	}
	defer func() {
		if err := handler.Close(); err != nil {
			log.Printf("close job library: %v", err)
		}
	}()
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorker := context.WithCancel(ctx)
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	mode := connectionMode(*simulate, *tcpAddress, *serialPort)
	var workerErr chan error
	workerFinished := false
	if mode != "" {
		workerErr = make(chan error, 1)
		endpoint := serverURL(listener.Addr())
		id := "local"
		if mode == "simulate" {
			id = "simulator"
		}
		connect := connectionConnector(mode, *tcpAddress, *serialPort, *baud)
		go func() {
			workerErr <- worker.Run(workerCtx, endpoint, key, id, connect)
		}()
	}
	defer func() {
		cancelWorker()
		if workerErr != nil && !workerFinished {
			if err := <-workerErr; err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("driver worker: %v", err)
			}
		}
	}()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	case err := <-workerErr:
		workerFinished = true
		if err != nil && !errors.Is(err, context.Canceled) {
			cancelWorker()
			_ = httpServer.Close()
			return fmt.Errorf("simulated driver: %w", err)
		}
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func runDriver(args []string) error {
	flags := flag.NewFlagSet("driver", flag.ContinueOnError)
	serverURLFlag := flags.String("server", "", "EmberDeck server URL")
	id := flags.String("id", "bench", "driver ID registered with EmberDeck")
	simulate := flags.Bool("simulate", false, "use a software GRBL simulator")
	tcpAddress := flags.String("tcp", "", "connect to a GRBL controller over TCP")
	serialPort := flags.String("serial", "", "connect to a serial controller")
	baud := flags.Int("baud", 115200, "serial connection baud rate")
	listPorts := flags.Bool("list-ports", false, "list available serial ports and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("driver does not accept positional arguments")
	}
	if *listPorts {
		if *simulate || *tcpAddress != "" || *serialPort != "" || *serverURLFlag != "" || flags.NArg() != 0 {
			return errors.New("-list-ports cannot be combined with connection or server options")
		}
		ports, err := transport.ListSerialPorts()
		if err != nil {
			return fmt.Errorf("list serial ports: %w", err)
		}
		if len(ports) == 0 {
			fmt.Println("No serial ports found")
		} else {
			for _, port := range ports {
				fmt.Println(port)
			}
		}
		return nil
	}
	if connectionCount(*simulate, *tcpAddress, *serialPort) != 1 {
		return errors.New("choose exactly one connection type: -simulate, -tcp address, or -serial port")
	}
	if *serialPort != "" && *baud <= 0 {
		return errors.New("-baud must be positive")
	}
	if err := validateServerURL(*serverURLFlag); err != nil {
		return err
	}
	key := os.Getenv("EMBERDECK_API_KEY")
	if key == "" {
		return errors.New("EMBERDECK_API_KEY is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := worker.Run(ctx, *serverURLFlag, key, *id, connectionConnector(connectionMode(*simulate, *tcpAddress, *serialPort), *tcpAddress, *serialPort, *baud))
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func connectionCount(simulate bool, tcpAddress, serialPort string) int {
	count := 0
	if simulate {
		count++
	}
	if tcpAddress != "" {
		count++
	}
	if serialPort != "" {
		count++
	}
	return count
}

func connectionMode(simulate bool, tcpAddress, serialPort string) string {
	switch {
	case simulate:
		return "simulate"
	case tcpAddress != "":
		return "tcp"
	case serialPort != "":
		return "serial"
	default:
		return ""
	}
}

func connectionConnector(mode, tcpAddress, serialPort string, baud int) worker.Connector {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch mode {
		case "simulate":
			return driver.NewSimulator(), nil
		case "tcp":
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", tcpAddress)
		case "serial":
			return transport.OpenSerial(ctx, serialPort, baud)
		default:
			return nil, errors.New("no driver connection type configured")
		}
	}
}

func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("-server must be an http or https URL without credentials, query, or fragment")
	}
	return nil
}

func serverURL(addr net.Addr) string {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return "http://127.0.0.1"
	}
	host := tcpAddr.IP.String()
	if tcpAddr.IP == nil || tcpAddr.IP.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(tcpAddr.Port))
}

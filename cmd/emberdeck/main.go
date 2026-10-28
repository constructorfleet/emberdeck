// EmberDeck manages laser cutter jobs and local or remote machine drivers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
		slog.Error(err.Error())
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
	metricsListenAddress := flags.String("metrics-listen", "", "optional Prometheus metrics listen address (disabled by default)")
	data := flags.String("data", "data", "job library directory")
	plugins := flags.String("plugins", "", "optional directory of plugins to load")
	simulate := flags.Bool("simulate", false, "run a simulated local driver")
	tcpAddress := flags.String("tcp", "", "run a driver connected to a GRBL TCP controller")
	serialPort := flags.String("serial", "", "run a driver connected to a serial controller")
	baud := flags.Int("baud", 115200, "serial connection baud rate")
	eventLimit := flags.Int("job-event-limit", server.DefaultEventLimit, "maximum number of job history events to keep (oldest are removed first)")
	eventRetention := flags.Duration("job-event-retention", 90*24*time.Hour, "maximum age of job history events; 0 keeps them until -job-event-limit removes them")
	serialDTR, serialRTS := addSerialModemFlags(flags)
	logLevel, logFormat := addLogFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve does not accept positional arguments")
	}
	if *eventLimit < 1 {
		return errors.New("-job-event-limit must be at least 1")
	}
	if *eventRetention < 0 {
		return errors.New("-job-event-retention must not be negative")
	}
	logger, err := newLogger(os.Stderr, *logLevel, *logFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	if connectionCount(*simulate, *tcpAddress, *serialPort) > 1 {
		return errors.New("choose at most one local driver connection: -simulate, -tcp address, or -serial port")
	}
	if *serialPort != "" && *baud <= 0 {
		return errors.New("-baud must be positive")
	}
	modemBits, err := parseSerialModemBits(*serialDTR, *serialRTS)
	if err != nil {
		return err
	}
	key := os.Getenv("EMBERDECK_API_KEY")
	if key == "" {
		return errors.New("EMBERDECK_API_KEY is required")
	}
	logger.Info("starting EmberDeck server", "version", version, "data", *data, "plugins", *plugins)
	handler, err := server.OpenWithOptions(*data, key, server.Options{
		PluginDir:      *plugins,
		Logger:         logger,
		EventLimit:     *eventLimit,
		EventRetention: *eventRetention,
	})
	if err != nil {
		return fmt.Errorf("open job library: %w", err)
	}
	defer func() {
		if err := handler.Close(); err != nil {
			logger.Error("close job library", "error", err)
		}
	}()
	handler.SetBuildVersion(version)
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	var metricsServer *http.Server
	var metricsServeErr chan error
	if *metricsListenAddress != "" {
		metricsListener, err := net.Listen("tcp", *metricsListenAddress)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("metrics listen: %w", err)
		}
		metricsServer = &http.Server{
			Handler:           handler.MetricsHandler(os.Getenv("EMBERDECK_METRICS_KEY")),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		logger.Info("serving metrics", "address", metricsListener.Addr().String(), "authenticated", os.Getenv("EMBERDECK_METRICS_KEY") != "")
		metricsServeErr = make(chan error, 1)
		go func() { metricsServeErr <- metricsServer.Serve(metricsListener) }()
		defer func() { _ = metricsServer.Close() }()
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
	defer cancelWorker()
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	logger.Info("serving HTTP", "address", listener.Addr().String())
	mode := connectionMode(*simulate, *tcpAddress, *serialPort)
	var workerErr chan error
	if mode != "" {
		workerErr = make(chan error, 1)
		endpoint := serverURL(listener.Addr())
		id := "local"
		if mode == "simulate" {
			id = "simulator"
		}
		label := localDriverLabel(mode, *tcpAddress, *serialPort)
		connect := connectionConnectorWithModemBits(mode, *tcpAddress, *serialPort, *baud, modemBits)
		logf := func(format string, args ...any) { logger.Info(fmt.Sprintf(format, args...), "driver", label) }
		logger.Info("starting local driver", "driver", label, "id", id)
		go func() {
			// A local worker failure marks the driver offline (its heartbeat
			// expires) and restarts it; it never stops the HTTP server.
			workerErr <- worker.Supervise(workerCtx, endpoint, key, id, connect, worker.Options{Logf: logf})
		}()
		defer func() {
			cancelWorker()
			if workerErr == nil {
				return
			}
			if err := <-workerErr; err != nil && !errors.Is(err, context.Canceled) {
				logf("%v", err)
			}
		}()
	}
	for waiting := true; waiting; {
		select {
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve HTTP: %w", err)
		case err := <-metricsServeErr:
			metricsServeErr = nil
			_ = httpServer.Close()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return fmt.Errorf("serve metrics: %w", err)
		case <-ctx.Done():
			waiting = false
		case err := <-workerErr:
			workerErr = nil
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("local driver stopped; driver offline, HTTP server still running", "driver", localDriverLabel(mode, *tcpAddress, *serialPort), "error", err)
			}
		}
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	if metricsServer != nil {
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			_ = metricsServer.Close()
			return fmt.Errorf("shut down metrics server: %w", err)
		}
		if err := <-metricsServeErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve metrics: %w", err)
		}
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
	serialDTR, serialRTS := addSerialModemFlags(flags)
	listPorts := flags.Bool("list-ports", false, "list available serial ports and exit")
	logLevel, logFormat := addLogFlags(flags)
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
	modemBits, err := parseSerialModemBits(*serialDTR, *serialRTS)
	if err != nil {
		return err
	}
	if err := validateServerURL(*serverURLFlag); err != nil {
		return err
	}
	logger, err := newLogger(os.Stderr, *logLevel, *logFormat)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	key := os.Getenv("EMBERDECK_API_KEY")
	if key == "" {
		return errors.New("EMBERDECK_API_KEY is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("starting EmberDeck driver", "version", version, "server", *serverURLFlag, "id", *id, "connection", localDriverLabel(connectionMode(*simulate, *tcpAddress, *serialPort), *tcpAddress, *serialPort))
	err = worker.Run(ctx, *serverURLFlag, key, *id, connectionConnectorWithModemBits(connectionMode(*simulate, *tcpAddress, *serialPort), *tcpAddress, *serialPort, *baud, modemBits))
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// addLogFlags registers -log-level and -log-format, defaulting to
// EMBERDECK_LOG_LEVEL and EMBERDECK_LOG_FORMAT so containers can set them.
func addLogFlags(flags *flag.FlagSet) (*string, *string) {
	level := flags.String("log-level", envOr("EMBERDECK_LOG_LEVEL", "info"), "log level: debug, info, warn, or error (env EMBERDECK_LOG_LEVEL)")
	format := flags.String("log-format", envOr("EMBERDECK_LOG_FORMAT", "text"), "log format: text or json (env EMBERDECK_LOG_FORMAT)")
	return level, format
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// newLogger builds a structured logger; empty settings mean info and text.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var minimum slog.Level
	switch level {
	case "debug":
		minimum = slog.LevelDebug
	case "", "info":
		minimum = slog.LevelInfo
	case "warn":
		minimum = slog.LevelWarn
	case "error":
		minimum = slog.LevelError
	default:
		return nil, fmt.Errorf("-log-level must be debug, info, warn, or error, not %q", level)
	}
	options := &slog.HandlerOptions{Level: minimum}
	switch format {
	case "", "text":
		return slog.New(slog.NewTextHandler(w, options)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, options)), nil
	default:
		return nil, fmt.Errorf("-log-format must be text or json, not %q", format)
	}
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

// localDriverLabel names the all-in-one driver's real transport for logs.
func localDriverLabel(mode, tcpAddress, serialPort string) string {
	switch mode {
	case "tcp":
		return "TCP driver " + tcpAddress
	case "serial":
		return "serial driver " + serialPort
	default:
		return "simulated driver"
	}
}

func connectionConnector(mode, tcpAddress, serialPort string, baud int) worker.Connector {
	return connectionConnectorWithModemBits(mode, tcpAddress, serialPort, baud, transport.ModemBits{})
}

func connectionConnectorWithModemBits(mode, tcpAddress, serialPort string, baud int, modemBits transport.ModemBits) worker.Connector {
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
			return transport.OpenSerialWithBits(ctx, serialPort, baud, modemBits)
		default:
			return nil, errors.New("no driver connection type configured")
		}
	}
}

func addSerialModemFlags(flags *flag.FlagSet) (*string, *string) {
	dtr := flags.String("serial-dtr", "off", "initial serial DTR output state (on or off; default off)")
	rts := flags.String("serial-rts", "off", "initial serial RTS output state (on or off; default off)")
	return dtr, rts
}

func parseSerialModemBits(dtr, rts string) (transport.ModemBits, error) {
	parse := func(flagName, value string) (bool, error) {
		switch value {
		case "off":
			return false, nil
		case "on":
			return true, nil
		default:
			return false, fmt.Errorf("-%s must be on or off", flagName)
		}
	}
	dtrOn, err := parse("serial-dtr", dtr)
	if err != nil {
		return transport.ModemBits{}, err
	}
	rtsOn, err := parse("serial-rts", rts)
	if err != nil {
		return transport.ModemBits{}, err
	}
	return transport.ModemBits{DTR: dtrOn, RTS: rtsOn}, nil
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

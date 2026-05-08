package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/aegis/internal/circuit"
	"github.com/user/aegis/internal/config"
	logpkg "github.com/user/aegis/internal/logging"
	"github.com/user/aegis/internal/metrics"
	"github.com/user/aegis/internal/pool"
	"github.com/user/aegis/internal/proxy"
	"github.com/user/aegis/internal/ratelimit"
	"github.com/user/aegis/internal/security"
	"github.com/user/aegis/internal/tui"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet(filepath.Base(os.Args[0]), flag.ContinueOnError)
	flags.SetOutput(os.Stdout)

	configPathShort := flags.String("c", "aegis.yml", "Path to config file")
	configPathLong := flags.String("config", "", "Path to config file")
	headless := flags.Bool("headless", false, "Run without TUI")
	logLevel := flags.String("log-level", "", "Override log level")
	showVersionShort := flags.Bool("v", false, "Show version")
	showVersionLong := flags.Bool("version", false, "Show version")
	showHelpShort := flags.Bool("h", false, "Show help")
	showHelpLong := flags.Bool("help", false, "Show help")

	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [flags]\n\n", filepath.Base(os.Args[0]))
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		return err
	}

	if *showHelpShort || *showHelpLong {
		flags.Usage()
		return nil
	}

	if *showVersionShort || *showVersionLong {
		fmt.Fprintln(os.Stdout, version)
		return nil
	}

	configPath := *configPathShort
	if *configPathLong != "" {
		configPath = *configPathLong
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if *logLevel != "" {
		cfg.Logging.Level = *logLevel
	}

	if *headless {
		cfg.TUI.Enabled = false
	}

	eventBuffer := logpkg.NewEventBuffer(10)
	if err := logpkg.ConfigureDefaultWithEvents(logpkg.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	}, eventBuffer, logWriter(cfg.TUI.Enabled)); err != nil {
		return fmt.Errorf("configure logger: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return runWithContext(ctx, cancel, cfg, eventBuffer)
}

func runWithContext(ctx context.Context, cancel context.CancelFunc, cfg *config.Config, eventBuffer *logpkg.EventBuffer) error {
	if ctx == nil {
		return fmt.Errorf("context must not be nil")
	}

	if cancel == nil {
		cancel = func() {}
	}

	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}

	if eventBuffer == nil {
		eventBuffer = logpkg.NewEventBuffer(10)
	}

	collector := metrics.NewMetricsCollector(time.Minute, 1000)
	backendPool, err := pool.NewPool(cfg.Backends, proxy.NewDirector, proxy.NewInstrumentedTransportFactory(collector))
	if err != nil {
		return err
	}
	attachCircuitBreakers(backendPool.GetAll(), cfg.CircuitBreaker)
	collector.SetBackends(backendPool.GetAll())

	pool.StartHealthChecks(ctx, backendPool, cfg.HealthCheck)
	// [SECURITY] Periodic logs emit backend-level aggregates only, avoiding client-controlled request details in telemetry output.
	metrics.StartPeriodicLoggingWithLevel(ctx, collector, 10*time.Second, cfg.Logging.Level)
	proxyHandler := proxy.NewProxyHandler(backendPool)
	rateLimiter := ratelimit.NewRateLimiter(cfg.RateLimit.RequestsPerSecond, float64(cfg.RateLimit.Burst))
	ratelimit.StartCleanup(ctx, rateLimiter, cfg.RateLimit.CleanupInterval)
	ratelimit.RunAdaptive(ctx, collector, rateLimiter, cfg.Adaptive)
	// [SECURITY] Rate limiting is enforced at the HTTP edge so abusive clients are rejected before backend resources are consumed.
	handler := security.SecurityHeaders(
		proxy.RequestLogger(
			proxy.Recovery(
				security.ValidateRequest(security.RequestValidationConfig{
					MaxBodyBytes: cfg.Server.MaxBodyBytes,
					AllowedHosts: cfg.Server.AllowedHosts,
				})(
					rateLimiter.Middleware(proxyHandler),
				),
			),
		),
	)

	server := &http.Server{
		Addr:           fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:        handler,
		ReadTimeout:    cfg.Server.ReadTimeout,  // [SECURITY] Slowloris protection at the edge.
		WriteTimeout:   cfg.Server.WriteTimeout, // [SECURITY] Slow read protection limits resource exhaustion.
		IdleTimeout:    cfg.Server.IdleTimeout,
		MaxHeaderBytes: cfg.Server.MaxHeaderBytes, // [SECURITY] Header size is capped to reduce request abuse.
	}

	slog.Info("aegis started", "port", cfg.Server.Port, "backends", len(backendPool.GetAll()))

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("listen and serve: %w", err)
			return
		}

		serverErr <- nil
	}()

	var program *tea.Program
	tuiErr := make(chan error, 1)
	if cfg.TUI.Enabled {
		model := tui.NewModelWithEventSource(collector.Snapshot, eventBuffer.Events, cfg.TUI.RefreshInterval)
		program = tea.NewProgram(model, tea.WithAltScreen())
		go func() {
			_, err := program.Run()
			tuiErr <- err
		}()
	}

	if err := waitForExit(ctx, cancel, serverErr, tuiErr, cfg.TUI.Enabled); err != nil {
		if program != nil {
			program.Quit()
		}
		return err
	}

	if program != nil {
		program.Quit()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()

	// [SECURITY] Graceful shutdown stops accepting new connections before process exit, limiting partial request handling under operator interrupts.
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}

	slog.Info("Aegis shutdown complete")
	return nil
}

func waitForExit(ctx context.Context, cancel context.CancelFunc, serverErr <-chan error, tuiErr <-chan error, tuiEnabled bool) error {
	for {
		select {
		case err := <-serverErr:
			cancel()
			return err
		case err := <-tuiErr:
			if !tuiEnabled {
				continue
			}
			if err == nil {
				cancel()
				return nil
			}

			tuiEnabled = false
			// [SECURITY] A local terminal failure must not make the proxy unavailable or skip graceful server shutdown.
			slog.Warn("tui failed; continuing headless", "error", err)
		case <-ctx.Done():
			return nil
		}
	}
}

func logWriter(tuiEnabled bool) io.Writer {
	if tuiEnabled {
		// [SECURITY] In alt-screen mode raw logs are captured in a bounded sanitized buffer and not written to the terminal sink.
		return io.Discard
	}

	return os.Stdout
}

func attachCircuitBreakers(backends []*pool.Backend, cfg config.CircuitBreakerConfig) {
	for _, backend := range backends {
		if backend == nil {
			continue
		}

		backend.CircuitBreaker = circuit.NewCircuitBreaker(cfg)
		if backend.URL != nil {
			// [SECURITY] Breaker identity is pinned to the trusted backend URL so state logs cannot be spoofed by client input.
			backend.CircuitBreaker.SetBackendName(backend.URL.String())
		}
	}
}

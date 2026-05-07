package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/user/aegis/internal/config"
	"github.com/user/aegis/internal/proxy"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
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

	targets, err := parseBackendTargets(cfg.Backends)
	if err != nil {
		return err
	}

	proxyHandler, err := proxy.NewProxyHandler(targets)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:           fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:        proxyHandler,
		ReadTimeout:    cfg.Server.ReadTimeout,  // [SECURITY] Slowloris protection at the edge.
		WriteTimeout:   cfg.Server.WriteTimeout, // [SECURITY] Slow read protection limits resource exhaustion.
		IdleTimeout:    cfg.Server.IdleTimeout,
		MaxHeaderBytes: cfg.Server.MaxHeaderBytes, // [SECURITY] Header size is capped to reduce request abuse.
	}

	log.Printf("Aegis started on :%d with %d backends", cfg.Server.Port, len(targets))

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}

	return nil
}

func parseBackendTargets(backends []config.BackendConfig) ([]proxy.Target, error) {
	targets := make([]proxy.Target, 0, len(backends))
	for _, backend := range backends {
		target, err := url.Parse(backend.URL)
		if err != nil {
			return nil, fmt.Errorf("parse backend target %q: %w", backend.URL, err)
		}

		targets = append(targets, proxy.Target{
			URL:         target,
			HostHeader:  backend.OriginalHost,
			DialAddress: backend.PinnedAddress,
			ServerName:  backend.ServerName,
		})
	}

	return targets, nil
}

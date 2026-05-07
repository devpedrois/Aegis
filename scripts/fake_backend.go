package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	portsFlag := flag.String("ports", "8081,8082,8083", "Comma-separated backend ports")
	flag.Parse()

	ports := parsePorts(*portsFlag)
	if len(ports) == 0 {
		log.Fatal("at least one port must be provided")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	servers := make([]*http.Server, 0, len(ports))

	for _, port := range ports {
		server := &http.Server{
			Addr:    ":" + port,
			Handler: newHandler(port),
		}

		servers = append(servers, server)
		wg.Add(1)

		go func(srv *http.Server, currentPort string) {
			defer wg.Done()
			log.Printf("fake backend listening on :%s", currentPort)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("fake backend :%s stopped with error: %v", currentPort, err)
			}
		}(server, port)
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error on %s: %v", server.Addr, err)
		}
	}

	wg.Wait()
}

func parsePorts(raw string) []string {
	parts := strings.Split(raw, ",")
	ports := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		ports = append(ports, trimmed)
	}

	return ports
}

func newHandler(port string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("backend localhost:%s received %s %s", port, r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"backend": fmt.Sprintf("localhost:%s", port),
			"path":    r.URL.Path,
		})
	})
}

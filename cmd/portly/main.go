package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/greatbody/portly/internal/config"
	"github.com/greatbody/portly/internal/server"
	"github.com/greatbody/portly/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	srv, err := server.New(cfg, st)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	username, generated, err := srv.EnsureAdmin()
	if err != nil {
		log.Fatalf("ensure admin: %v", err)
	}
	fmt.Println("====================================================")
	fmt.Println("  portly is starting")
	fmt.Printf("  Listen:    %s\n", cfg.Listen)
	if cfg.PublicBaseURL != "" {
		fmt.Printf("  Public:    %s\n", cfg.PublicBaseURL)
	}
	fmt.Printf("  Database:  %s\n", cfg.DatabasePath)
	fmt.Printf("  Admin:     %s\n", username)
	if generated != "" {
		fmt.Printf("  Password:  %s   (generated; save it now!)\n", generated)
	} else {
		fmt.Println("  Password:  (existing user; not shown)")
	}
	fmt.Println("====================================================")

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

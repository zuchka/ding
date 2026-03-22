package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/super-ding/ding/internal/server"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:   "ding",
		Short: "DING — stream-based alerting daemon",
	}

	var configPath string

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the alerting daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(configPath)
		},
	}
	serveCmd.Flags().StringVar(&configPath, "config", "ding.yaml", "path to config file")

	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(configPath)
		},
	}
	validateCmd.Flags().StringVar(&configPath, "config", "ding.yaml", "path to config file")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("ding version", version)
		},
	}

	root.AddCommand(serveCmd, validateCmd, versionCmd)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func runValidate(configPath string) error {
	_, _, _, err := server.BuildFromConfig(configPath)
	if err != nil {
		return fmt.Errorf("config invalid: %w", err)
	}
	fmt.Println("config OK:", configPath)
	return nil
}

func runServe(configPath string) error {
	eng, cfg, notifiers, err := server.BuildFromConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	srv := server.New(eng, notifiers, cfg, configPath)

	// Detect stdin pipe
	stdinInfo, err := os.Stdin.Stat()
	if err == nil && (stdinInfo.Mode()&os.ModeCharDevice) == 0 {
		go readStdin(srv, cfg.Server.Format)
	}

	httpSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: srv.Handler(),
	}

	// Signal handling
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	go func() {
		for range sighup {
			log.Println("ding: received SIGHUP, reloading config...")
			newEng, newCfg, newNotifiers, err := server.BuildFromConfig(configPath)
			if err != nil {
				log.Printf("ding: reload failed: %v (keeping current config)", err)
				continue
			}
			srv.SwapEngine(newEng, newCfg, newNotifiers)
			log.Printf("ding: config reloaded from %s", configPath)
		}
	}()

	log.Printf("ding: listening on :%d (config: %s)", cfg.Server.Port, configPath)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ding: server error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Println("ding: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func readStdin(srv *server.Server, _ string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// POST the line to /ingest via the handler directly
		srv.IngestLine(line)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("ding: stdin read error: %v", err)
	}
	// stdin EOF — HTTP server continues
}

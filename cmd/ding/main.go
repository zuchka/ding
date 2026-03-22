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

	"github.com/super-ding/ding/internal/evaluator"
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

	// Persistence: restore state and start periodic flusher.
	stopFlusher := func() {} // no-op until flusher is started
	if cfg.Persistence.StateFile != "" {
		snap, err := evaluator.LoadSnapshot(cfg.Persistence.StateFile)
		if err != nil {
			log.Printf("ding: could not load state: %v (starting fresh)", err)
		} else if snap != nil {
			evaluator.RestoreEngine(eng, *snap, time.Now())
			log.Printf("ding: restored state from %s (saved at %s)", cfg.Persistence.StateFile, snap.SavedAt.Format(time.RFC3339))
		}
		stopFlusher = eng.StartFlusher(cfg.Persistence.StateFile, cfg.Persistence.FlushInterval.Duration)
	}

	// Set up the reload hook so /reload endpoint also transfers state.
	srv.SetReloadHook(func() error {
		// Flush old engine state to disk.
		stopFlusher()
		stopFlusher = func() {}

		newEng, newCfg, newNotifiers, err := server.BuildFromConfig(configPath)
		if err != nil {
			// Restart flusher on old engine since reload failed.
			if cfg.Persistence.StateFile != "" {
				stopFlusher = eng.StartFlusher(cfg.Persistence.StateFile, cfg.Persistence.FlushInterval.Duration)
			}
			return fmt.Errorf("reload failed: %w", err)
		}

		// Restore state into new engine from file.
		if newCfg.Persistence.StateFile != "" {
			snap, err := evaluator.LoadSnapshot(newCfg.Persistence.StateFile)
			if err != nil {
				log.Printf("ding: state restore after reload failed: %v (new engine starts fresh)", err)
			} else if snap != nil {
				evaluator.RestoreEngine(newEng, *snap, time.Now())
			}
		}

		srv.SwapEngine(newEng, newCfg, newNotifiers)
		eng = newEng
		cfg = newCfg
		notifiers = newNotifiers
		log.Printf("ding: config reloaded from %s", configPath)

		// Start new flusher on new engine.
		if newCfg.Persistence.StateFile != "" {
			stopFlusher = newEng.StartFlusher(newCfg.Persistence.StateFile, newCfg.Persistence.FlushInterval.Duration)
		}
		return nil
	})

	// Detect stdin pipe
	stdinInfo, err := os.Stdin.Stat()
	if err == nil && (stdinInfo.Mode()&os.ModeCharDevice) == 0 {
		go readStdin(srv, cfg.Server.Format)
	}

	httpSrv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      srv.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout.Duration,
		WriteTimeout: cfg.Server.WriteTimeout.Duration,
		IdleTimeout:  cfg.Server.IdleTimeout.Duration,
	}

	// Signal handling
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	go func() {
		for range sighup {
			log.Println("ding: received SIGHUP, reloading config...")

			// 1a. Flush old engine state to disk.
			stopFlusher()
			// 1b. Reset to no-op immediately so any early return path is safe.
			stopFlusher = func() {}

			newEng, newCfg, newNotifiers, err := server.BuildFromConfig(configPath)
			if err != nil {
				log.Printf("ding: reload failed: %v (keeping current config)", err)
				// Restart flusher on old engine since reload failed.
				if cfg.Persistence.StateFile != "" {
					stopFlusher = eng.StartFlusher(cfg.Persistence.StateFile, cfg.Persistence.FlushInterval.Duration)
				}
				continue
			}

			// Restore state into new engine from file.
			if newCfg.Persistence.StateFile != "" {
				snap, err := evaluator.LoadSnapshot(newCfg.Persistence.StateFile)
				if err != nil {
					log.Printf("ding: state restore after reload failed: %v (new engine starts fresh)", err)
				} else if snap != nil {
					evaluator.RestoreEngine(newEng, *snap, time.Now())
				}
			}

			srv.SwapEngine(newEng, newCfg, newNotifiers)
			eng = newEng
			cfg = newCfg
			notifiers = newNotifiers
			log.Printf("ding: config reloaded from %s", configPath)

			// Start new flusher on new engine.
			if newCfg.Persistence.StateFile != "" {
				stopFlusher = newEng.StartFlusher(newCfg.Persistence.StateFile, newCfg.Persistence.FlushInterval.Duration)
			}
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
	signal.Stop(sighup)
	close(sighup)
	log.Println("ding: shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("ding: shutdown error: %v", err)
	}

	// Stop webhook goroutines, then do final state flush.
	for _, n := range notifiers {
		if stopper, ok := n.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	}
	stopFlusher()
	return nil
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

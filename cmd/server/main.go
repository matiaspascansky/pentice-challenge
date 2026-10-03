// Command server expone la API de confirmación de reservas.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"pentice-challenge/internal/client"
	"pentice-challenge/internal/config"
	"pentice-challenge/internal/handler"
	"pentice-challenge/internal/repository/file"
	"pentice-challenge/internal/service"
	"pentice-challenge/internal/worker"
)

const (
	// workerBatch es cuántas entregas vencidas se toman por tick.
	workerBatch = 100
	// workerConcurrency es el tamaño del pool de intentos simultáneos.
	workerConcurrency = 4
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// El contexto se cancela con SIGINT/SIGTERM y maneja el shutdown ordenado.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Repositorio en archivo: las entregas pendientes sobreviven al restart.
	repo, err := file.New(ctx, cfg.DataFile)
	if err != nil {
		return err
	}

	bookings := service.NewBookingService(repo, service.CryptoCodeGenerator{})
	deliveries := service.NewDeliveryService(
		repo,
		client.NewWebhookNotifier(cfg.GuestWebhookURL),
		cfg.BackoffBase,
		cfg.BackoffMax,
		cfg.NotifyTimeout,
	)

	// El worker arranca antes del server: si quedaron entregas pendientes de
	// una corrida anterior, se retoman enseguida.
	deliveryWorker := worker.New(deliveries, worker.Config{
		Tick:        cfg.WorkerTick,
		Batch:       workerBatch,
		Concurrency: workerConcurrency,
	})

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		deliveryWorker.Run(ctx)
	}()

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: handler.NewRouter(
			handler.NewBookingHandler(bookings),
			handler.NewDeliveryHandler(deliveries),
		),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "port", cfg.Port, "dataFile", cfg.DataFile)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Primero dejamos de aceptar requests, después esperamos a que el worker
	// termine los intentos que tiene en vuelo.
	err = srv.Shutdown(shutdownCtx)
	workers.Wait()
	return err
}

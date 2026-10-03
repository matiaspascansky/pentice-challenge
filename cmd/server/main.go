// Command server expone la API de confirmación de reservas.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pentice-challenge/internal/config"
	"pentice-challenge/internal/handler"
	"pentice-challenge/internal/repository/file"
	"pentice-challenge/internal/service"
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
	bookingHandler := handler.NewBookingHandler(bookings)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler.NewRouter(bookingHandler),
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
	return srv.Shutdown(shutdownCtx)
}

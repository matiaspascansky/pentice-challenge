// Package worker contiene los procesos de fondo del servicio.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/service"
)

// Config son los parámetros del worker de entregas.
type Config struct {
	// Tick es cada cuánto se escanean las entregas vencidas.
	Tick time.Duration
	// Batch es el máximo de entregas que se toman por tick.
	Batch int
	// Concurrency es el tamaño del pool: un guest lento no debe frenar al resto.
	Concurrency int
}

// DeliveryWorker reintenta las entregas pendientes hasta que llega el ack.
//
// A esta escala un ticker que escanea las entregas vencidas es lo más simple
// que funciona. En producción esto sería un relay del outbox hacia una cola
// real (SQS/Kafka) con los reintentos delegados a la cola.
type DeliveryWorker struct {
	deliveries *service.DeliveryService
	cfg        Config

	// inFlight evita que dos ticks tomen la misma entrega en paralelo: un
	// intento puede durar más que un tick.
	mu       sync.Mutex
	inFlight map[string]struct{}
}

// New arma el worker.
func New(deliveries *service.DeliveryService, cfg Config) *DeliveryWorker {
	return &DeliveryWorker{
		deliveries: deliveries,
		cfg:        cfg,
		inFlight:   make(map[string]struct{}),
	}
}

// Run bloquea hasta que ctx se cancela, y antes de volver espera a que
// terminen los intentos en vuelo (parte del shutdown ordenado).
func (w *DeliveryWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Tick)
	defer ticker.Stop()

	slog.Info("delivery worker started",
		"tick", w.cfg.Tick,
		"batch", w.cfg.Batch,
		"concurrency", w.cfg.Concurrency)

	// Semáforo: acota cuántos intentos corren a la vez.
	slots := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			slog.Info("delivery worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx, &wg, slots)
		}
	}
}

// tick toma las entregas vencidas y las despacha al pool. No bloquea esperando
// a que terminen, para que el ticker siga corriendo.
func (w *DeliveryWorker) tick(ctx context.Context, wg *sync.WaitGroup, slots chan struct{}) {
	due, err := w.deliveries.Due(ctx, time.Now().UTC(), w.cfg.Batch)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("scanning due deliveries", "error", err)
		}
		return
	}

	for _, d := range due {
		if !w.claim(d.Code) {
			continue // ya hay un intento en vuelo para este código
		}

		wg.Add(1)
		go func(d model.Delivery) {
			defer wg.Done()
			defer w.release(d.Code)

			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}

			w.attempt(ctx, d)
		}(d)
	}
}

// attempt hace un intento y lo loguea.
func (w *DeliveryWorker) attempt(ctx context.Context, d model.Delivery) {
	err := w.deliveries.Attempt(ctx, d)

	// Releemos el estado para loguear cuándo quedó agendado el próximo intento.
	next := "n/a"
	if updated, statusErr := w.deliveries.Status(context.WithoutCancel(ctx), d.Code); statusErr == nil {
		next = updated.NextAttemptAt.Format(time.RFC3339)
		if updated.Status == model.DeliveryAcked {
			next = "none (acked)"
		}
	}

	if err != nil {
		slog.Warn("delivery attempt failed",
			"confirmationCode", d.Code,
			"attempt", d.Attempts+1,
			"error", err,
			"nextAttemptAt", next)
		return
	}

	slog.Info("delivery attempt sent",
		"confirmationCode", d.Code,
		"attempt", d.Attempts+1,
		"nextAttemptAt", next)
}

// claim marca el código como en vuelo. Devuelve false si ya lo estaba.
func (w *DeliveryWorker) claim(code string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, busy := w.inFlight[code]; busy {
		return false
	}
	w.inFlight[code] = struct{}{}
	return true
}

func (w *DeliveryWorker) release(code string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.inFlight, code)
}

package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository/memory"
	"pentice-challenge/internal/service"
	"pentice-challenge/internal/worker"
)

// deviceNotifier simula el dispositivo del huésped: falla las primeras N
// notificaciones y, cuando por fin recibe una, ackea (como haría el guest).
type deviceNotifier struct {
	mu        sync.Mutex
	received  []model.Notification
	failFirst int
	onReceive func(n model.Notification)
}

func (d *deviceNotifier) Notify(_ context.Context, n model.Notification) error {
	d.mu.Lock()
	d.received = append(d.received, n)
	shouldFail := len(d.received) <= d.failFirst
	onReceive := d.onReceive
	d.mu.Unlock()

	if shouldFail {
		return errors.New("device unreachable")
	}
	if onReceive != nil {
		onReceive(n)
	}
	return nil
}

func (d *deviceNotifier) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.received)
}

// testConfig usa tiempos diminutos para que el test corra rápido.
func testConfig() worker.Config {
	return worker.Config{Tick: 2 * time.Millisecond, Batch: 10, Concurrency: 4}
}

// waitFor espera hasta que cond se cumpla, o falla el test.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// El caso 5 de la consigna: se reintenta hasta que llega el ack, y después se
// deja de reintentar.
func TestWorkerRetriesUntilAcked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := memory.New()
	bookings := service.NewBookingService(repo, service.CryptoCodeGenerator{})
	booking, _, err := bookings.Create(ctx, "R1", "ana@example.com")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	device := &deviceNotifier{failFirst: 3}
	deliveries := service.NewDeliveryService(repo, device, time.Millisecond, 5*time.Millisecond, time.Second)
	// Cuando la notificación por fin llega, el dispositivo ackea.
	device.onReceive = func(n model.Notification) {
		if err := deliveries.Ack(context.Background(), n.Code); err != nil {
			t.Errorf("Ack() error = %v", err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.New(deliveries, testConfig()).Run(ctx)
	}()

	waitFor(t, 5*time.Second, "la entrega quedara acked", func() bool {
		d, err := deliveries.Status(ctx, booking.Code)
		return err == nil && d.Status == model.DeliveryAcked
	})

	attemptsAtAck := device.count()
	if attemptsAtAck < 4 {
		t.Errorf("notifications = %d, want at least 4 (3 fallidas + 1 entregada)", attemptsAtAck)
	}

	// Lo importante: después del ack no se vuelve a notificar.
	time.Sleep(100 * time.Millisecond)
	if after := device.count(); after != attemptsAtAck {
		t.Errorf("notifications = %d after the ack, want it to stay at %d", after, attemptsAtAck)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run() did not return after the context was cancelled")
	}
}

// Un 200 del webhook no corta los reintentos: sin ack, se sigue intentando.
func TestWorkerKeepsRetryingWithoutAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := memory.New()
	bookings := service.NewBookingService(repo, service.CryptoCodeGenerator{})
	if _, _, err := bookings.Create(ctx, "R1", "ana@example.com"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Recibe todo correctamente, pero nunca ackea.
	device := &deviceNotifier{}
	deliveries := service.NewDeliveryService(repo, device, time.Millisecond, 5*time.Millisecond, time.Second)

	go worker.New(deliveries, testConfig()).Run(ctx)

	waitFor(t, 5*time.Second, "varios reintentos pese a los 200", func() bool {
		return device.count() >= 3
	})
}

func TestWorkerProcessesEachDeliveryOnceAtATime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := memory.New()
	bookings := service.NewBookingService(repo, service.CryptoCodeGenerator{})
	if _, _, err := bookings.Create(ctx, "R1", "ana@example.com"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Notificador lento: dura mucho más que un tick, así que si el worker no
	// marcara las entregas en vuelo, varios ticks tomarían la misma.
	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
	)
	device := &blockingNotifier{onNotify: func() {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()

		time.Sleep(60 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
	}}

	deliveries := service.NewDeliveryService(repo, device, time.Millisecond, 2*time.Millisecond, time.Second)
	go worker.New(deliveries, testConfig()).Run(ctx)

	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 1 {
		t.Errorf("the same delivery was attempted %d times in parallel, want 1", maxSeen)
	}
}

type blockingNotifier struct {
	onNotify func()
}

func (b *blockingNotifier) Notify(_ context.Context, _ model.Notification) error {
	b.onNotify()
	return nil
}

func TestWorkerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	deliveries := service.NewDeliveryService(memory.New(), &deviceNotifier{}, time.Millisecond, time.Millisecond, time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.New(deliveries, testConfig()).Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run() did not return after the context was cancelled")
	}
}

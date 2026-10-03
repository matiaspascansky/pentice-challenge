package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository"
	"pentice-challenge/internal/repository/memory"
	"pentice-challenge/internal/service"
)

// fakeNotifier registra las notificaciones y puede fallar las primeras N,
// para simular un dispositivo que todavía no está disponible.
type fakeNotifier struct {
	mu        sync.Mutex
	calls     []model.Notification
	failFirst int
}

func (f *fakeNotifier) Notify(_ context.Context, n model.Notification) error {
	f.mu.Lock()
	f.calls = append(f.calls, n)
	shouldFail := len(f.calls) <= f.failFirst
	f.mu.Unlock()

	if shouldFail {
		return errors.New("device unreachable")
	}
	return nil
}

// newDeliveryFixture arma un repo con un booking listo para entregar.
func newDeliveryFixture(t *testing.T, notifier service.Notifier) (*repository.Store, *service.DeliveryService, string) {
	t.Helper()

	repo := memory.New()
	bookings := service.NewBookingService(repo, &scriptedGen{codes: []string{"a2b34"}})
	booking, _, err := bookings.Create(context.Background(), "R1", "ana@example.com")
	if err != nil {
		t.Fatalf("seeding booking: %v", err)
	}

	deliveries := service.NewDeliveryService(repo, notifier, time.Second, 30*time.Second, time.Second)
	return repo, deliveries, booking.Code
}

// Un 200 del webhook no es un ack: solo dice que el request llegó. Si dejáramos
// de reintentar acá, una notificación que el dispositivo recibió pero no
// procesó se perdería para siempre.
func TestAttemptSchedulesNextEvenWhenNotifySucceeds(t *testing.T) {
	ctx := context.Background()
	notifier := &fakeNotifier{}
	repo, deliveries, code := newDeliveryFixture(t, notifier)

	before, err := repo.Get(ctx, code)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if err := deliveries.Attempt(ctx, before); err != nil {
		t.Fatalf("Attempt() error = %v", err)
	}

	after, err := repo.Get(ctx, code)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if after.Status != model.DeliverySent {
		t.Errorf("status = %q, want %q", after.Status, model.DeliverySent)
	}
	if after.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", after.Attempts)
	}
	if after.LastError != "" {
		t.Errorf("lastError = %q, want empty", after.LastError)
	}
	if !after.NextAttemptAt.After(before.NextAttemptAt) {
		t.Error("next attempt was not rescheduled: los reintentos se cortarían sin ack")
	}
}

func TestAttemptRecordsFailure(t *testing.T) {
	ctx := context.Background()
	notifier := &fakeNotifier{failFirst: 1}
	repo, deliveries, code := newDeliveryFixture(t, notifier)

	delivery, _ := repo.Get(ctx, code)
	if err := deliveries.Attempt(ctx, delivery); err == nil {
		t.Fatal("Attempt() error = nil, want the notifier error")
	}

	after, _ := repo.Get(ctx, code)
	if after.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", after.Attempts)
	}
	if after.LastError == "" {
		t.Error("lastError is empty, want the notifier error")
	}
}

// Una vez acked, un intento en vuelo que termina después no debe resucitar la
// entrega: es la carrera entre el ack y el intento.
func TestAttemptDoesNotResurrectAckedDelivery(t *testing.T) {
	ctx := context.Background()
	notifier := &fakeNotifier{}
	repo, deliveries, code := newDeliveryFixture(t, notifier)

	delivery, _ := repo.Get(ctx, code)
	if err := deliveries.Ack(ctx, code); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}

	if err := deliveries.Attempt(ctx, delivery); err != nil {
		t.Fatalf("Attempt() error = %v", err)
	}

	after, _ := repo.Get(ctx, code)
	if after.Status != model.DeliveryAcked {
		t.Errorf("status = %q, want it to stay %q", after.Status, model.DeliveryAcked)
	}
	if after.Attempts != 0 {
		t.Errorf("attempts = %d, want 0: no debería registrarse sobre una acked", after.Attempts)
	}
}

func TestAckIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo, deliveries, code := newDeliveryFixture(t, &fakeNotifier{})

	if err := deliveries.Ack(ctx, code); err != nil {
		t.Fatalf("first Ack() error = %v", err)
	}
	first, _ := repo.Get(ctx, code)

	// El dispositivo re-ackea los duplicados, así que el mismo ack llega
	// muchas veces y no debe romper nada ni mover el ackedAt.
	if err := deliveries.Ack(ctx, code); err != nil {
		t.Fatalf("second Ack() error = %v", err)
	}
	second, _ := repo.Get(ctx, code)

	if second.AckedAt == nil || !second.AckedAt.Equal(*first.AckedAt) {
		t.Errorf("ackedAt = %v, want it unchanged at %v", second.AckedAt, first.AckedAt)
	}
}

func TestAckValidatesAndReportsMissing(t *testing.T) {
	ctx := context.Background()
	_, deliveries, code := newDeliveryFixture(t, &fakeNotifier{})

	t.Run("case-insensitive", func(t *testing.T) {
		if err := deliveries.Ack(ctx, "A2B34"); err != nil {
			t.Errorf("Ack(A2B34) error = %v, want nil for %q", err, code)
		}
	})

	t.Run("formato inválido", func(t *testing.T) {
		if err := deliveries.Ack(ctx, "nope"); !errors.Is(err, model.ErrInvalidInput) {
			t.Errorf("Ack() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("código inexistente", func(t *testing.T) {
		if err := deliveries.Ack(ctx, "zzzzz"); !errors.Is(err, model.ErrNotFound) {
			t.Errorf("Ack() error = %v, want ErrNotFound", err)
		}
	})
}

func TestNextAttemptAtBackoff(t *testing.T) {
	const (
		base = 100 * time.Millisecond
		max  = time.Second
	)
	deliveries := service.NewDeliveryService(memory.New(), &fakeNotifier{}, base, max, time.Second)
	now := time.Now().UTC()

	tests := []struct {
		attempts int
		want     time.Duration // antes del jitter
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, time.Second}, // topeado
		{10, time.Second},
		{1000, time.Second}, // no desborda
	}

	for _, tc := range tests {
		// Varias corridas: el jitter es aleatorio.
		for i := 0; i < 50; i++ {
			delay := deliveries.NextAttemptAt(now, tc.attempts).Sub(now)

			if delay > max {
				t.Fatalf("attempts=%d: delay = %v, want <= max (%v)", tc.attempts, delay, max)
			}
			lo := time.Duration(float64(tc.want) * 0.8)
			hi := time.Duration(float64(tc.want) * 1.2)
			if hi > max {
				hi = max
			}
			if delay < lo || delay > hi {
				t.Fatalf("attempts=%d: delay = %v, want within ±20%% of %v ([%v, %v])", tc.attempts, delay, tc.want, lo, hi)
			}
		}
	}
}

func TestNextAttemptAtAppliesJitter(t *testing.T) {
	deliveries := service.NewDeliveryService(memory.New(), &fakeNotifier{}, time.Second, time.Minute, time.Second)
	now := time.Now().UTC()

	seen := make(map[time.Duration]bool)
	for i := 0; i < 50; i++ {
		seen[deliveries.NextAttemptAt(now, 0).Sub(now)] = true
	}
	// Sin jitter todas las esperas serían idénticas y muchas entregas vencidas
	// saldrían en bloque.
	if len(seen) < 10 {
		t.Errorf("got %d distinct delays out of 50, jitter does not look applied", len(seen))
	}
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	_, deliveries, code := newDeliveryFixture(t, &fakeNotifier{})

	got, err := deliveries.Status(ctx, code)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != model.DeliveryPending {
		t.Errorf("status = %q, want %q", got.Status, model.DeliveryPending)
	}

	if _, err := deliveries.Status(ctx, "nope"); !errors.Is(err, model.ErrInvalidInput) {
		t.Errorf("Status(nope) error = %v, want ErrInvalidInput", err)
	}
	if _, err := deliveries.Status(ctx, "zzzzz"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("Status(zzzzz) error = %v, want ErrNotFound", err)
	}
}

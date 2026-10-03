package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository/memory"
	"pentice-challenge/internal/service"
)

// scriptedGen devuelve códigos predefinidos, repitiendo el último cuando se
// agota la lista. Permite forzar colisiones de forma determinista.
type scriptedGen struct {
	mu    sync.Mutex
	codes []string
	calls int
}

func (g *scriptedGen) Generate() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.calls++
	if len(g.codes) == 0 {
		return "", errors.New("no codes scripted")
	}
	if g.calls > len(g.codes) {
		return g.codes[len(g.codes)-1], nil
	}
	return g.codes[g.calls-1], nil
}

func TestCreateIsIdempotentByReservationID(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	gen := &scriptedGen{codes: []string{"aaaaa", "bbbbb"}}
	svc := service.NewBookingService(repo, gen)

	first, created, err := svc.Create(ctx, "R1", "ana@example.com")
	if err != nil || !created {
		t.Fatalf("first Create() = (%v, %v, %v), want created", first, created, err)
	}

	second, created, err := svc.Create(ctx, "R1", "ana@example.com")
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if created {
		t.Error("second Create() created a new booking, want the existing one")
	}
	if second.Code != first.Code {
		t.Errorf("second Create() code = %q, want %q: el huésped recibiría dos confirmaciones distintas", second.Code, first.Code)
	}

	// Una sola entrega: si hubiera dos, el huésped recibiría dos códigos.
	if _, err := repo.Get(ctx, "bbbbb"); !errors.Is(err, model.ErrNotFound) {
		t.Error("a second delivery was created for the same reservation")
	}
	due, err := repo.Due(ctx, first.CreatedAt, 0)
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 1 {
		t.Errorf("got %d deliveries, want 1", len(due))
	}
}

func TestCreateRegeneratesOnCollision(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	// El tercer código es el que finalmente debería usarse: los dos primeros
	// chocan con el booking que ya existe.
	gen := &scriptedGen{codes: []string{"aaaaa", "aaaaa", "aaaaa", "bbbbb"}}
	svc := service.NewBookingService(repo, gen)

	if _, _, err := svc.Create(ctx, "R1", "ana@example.com"); err != nil {
		t.Fatalf("Create(R1) error = %v", err)
	}

	second, created, err := svc.Create(ctx, "R2", "beto@example.com")
	if err != nil || !created {
		t.Fatalf("Create(R2) = (%v, %v, %v), want created", second, created, err)
	}
	if second.Code != "bbbbb" {
		t.Errorf("Create(R2) code = %q, want %q", second.Code, "bbbbb")
	}
	if gen.calls != 4 {
		t.Errorf("Generate() called %d times, want 4 (1 + 2 colisiones + 1)", gen.calls)
	}
}

func TestCreateFailsWhenCodesKeepColliding(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	gen := &scriptedGen{codes: []string{"aaaaa"}} // siempre el mismo
	svc := service.NewBookingService(repo, gen)

	if _, _, err := svc.Create(ctx, "R1", "ana@example.com"); err != nil {
		t.Fatalf("Create(R1) error = %v", err)
	}

	if _, _, err := svc.Create(ctx, "R2", "beto@example.com"); err == nil {
		t.Fatal("Create(R2) succeeded, want an error after exhausting the attempts")
	}
}

func TestCreateValidatesInput(t *testing.T) {
	ctx := context.Background()
	svc := service.NewBookingService(memory.New(), &scriptedGen{codes: []string{"aaaaa"}})

	tests := map[string]struct {
		reservationID, guest string
	}{
		"sin reservationId":       {"", "ana@example.com"},
		"sin guest":               {"R1", ""},
		"reservationId en blanco": {"   ", "ana@example.com"},
		"guest en blanco":         {"R1", "\t"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := svc.Create(ctx, tc.reservationID, tc.guest)
			if !errors.Is(err, model.ErrInvalidInput) {
				t.Errorf("Create() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestGetByCode(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	svc := service.NewBookingService(repo, &scriptedGen{codes: []string{"a2b34"}})

	if _, _, err := svc.Create(ctx, "R1", "ana@example.com"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	t.Run("encuentra el booking", func(t *testing.T) {
		got, err := svc.GetByCode(ctx, "a2b34")
		if err != nil {
			t.Fatalf("GetByCode() error = %v", err)
		}
		if got.ReservationID != "R1" || got.Guest != "ana@example.com" {
			t.Errorf("GetByCode() = %+v, want R1/ana@example.com", got)
		}
	})

	t.Run("es case-insensitive", func(t *testing.T) {
		if _, err := svc.GetByCode(ctx, "A2B34"); err != nil {
			t.Errorf("GetByCode(A2B34) error = %v, want the booking", err)
		}
	})

	t.Run("formato inválido", func(t *testing.T) {
		if _, err := svc.GetByCode(ctx, "nope"); !errors.Is(err, model.ErrInvalidInput) {
			t.Errorf("GetByCode() error = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("código inexistente", func(t *testing.T) {
		if _, err := svc.GetByCode(ctx, "zzzzz"); !errors.Is(err, model.ErrNotFound) {
			t.Errorf("GetByCode() error = %v, want ErrNotFound", err)
		}
	})
}

// Correr con -race: es el test que protege el check+insert atómico del
// repositorio contra requests concurrentes.
func TestCreateConcurrentlyYieldsUniqueCodes(t *testing.T) {
	ctx := context.Background()
	repo := memory.New()
	svc := service.NewBookingService(repo, service.CryptoCodeGenerator{})

	const n = 300
	var wg sync.WaitGroup
	codes := make(chan string, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, created, err := svc.Create(ctx, fmt.Sprintf("R%04d", i), "guest@example.com")
			if err != nil {
				t.Errorf("Create() error = %v", err)
				return
			}
			if !created {
				t.Errorf("Create(R%04d) returned an existing booking", i)
				return
			}
			codes <- b.Code
		}(i)
	}
	wg.Wait()
	close(codes)

	seen := make(map[string]bool, n)
	for code := range codes {
		if seen[code] {
			t.Fatalf("code %q was assigned to two reservations", code)
		}
		seen[code] = true
	}
	if len(seen) != n {
		t.Errorf("got %d codes, want %d", len(seen), n)
	}
}

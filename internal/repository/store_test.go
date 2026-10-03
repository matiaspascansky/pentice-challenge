package repository_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository"
	"pentice-challenge/internal/repository/file"
	"pentice-challenge/internal/repository/memory"
)

// implementations son las dos implementaciones del repositorio: el contrato
// tiene que cumplirse igual en ambas.
func implementations(t *testing.T) map[string]func() *repository.Store {
	t.Helper()

	return map[string]func() *repository.Store{
		"memory": memory.New,
		"file": func() *repository.Store {
			store, err := file.New(context.Background(), filepath.Join(t.TempDir(), "store.json"))
			if err != nil {
				t.Fatalf("file.New() error = %v", err)
			}
			return store
		},
	}
}

func booking(code, reservationID string) model.Booking {
	return model.Booking{
		Code:          code,
		ReservationID: reservationID,
		Guest:         reservationID + "@example.com",
		CreatedAt:     time.Now().UTC(),
	}
}

func delivery(code string, nextAttemptAt time.Time) model.Delivery {
	return model.Delivery{
		Code:          code,
		Status:        model.DeliveryPending,
		NextAttemptAt: nextAttemptAt,
	}
}

func TestCreateWithDelivery(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore()
			now := time.Now().UTC()

			t.Run("crea booking y delivery juntos", func(t *testing.T) {
				got, created, err := store.CreateWithDelivery(ctx, booking("aaaaa", "R1"), delivery("aaaaa", now))
				if err != nil || !created {
					t.Fatalf("CreateWithDelivery() = (%v, %v, %v), want created", got, created, err)
				}
				if _, err := store.GetByCode(ctx, "aaaaa"); err != nil {
					t.Errorf("GetByCode() error = %v", err)
				}
				if _, err := store.Get(ctx, "aaaaa"); err != nil {
					t.Errorf("Get() error = %v: la entrega debe guardarse junto al booking", err)
				}
			})

			t.Run("misma reserva devuelve el booking existente", func(t *testing.T) {
				got, created, err := store.CreateWithDelivery(ctx, booking("bbbbb", "R1"), delivery("bbbbb", now))
				if err != nil {
					t.Fatalf("CreateWithDelivery() error = %v", err)
				}
				if created {
					t.Error("created = true, want false for a repeated reservation")
				}
				if got.Code != "aaaaa" {
					t.Errorf("code = %q, want the original %q", got.Code, "aaaaa")
				}
				if _, err := store.Get(ctx, "bbbbb"); !errors.Is(err, model.ErrNotFound) {
					t.Error("a second delivery was stored for the same reservation")
				}
			})

			t.Run("código tomado por otra reserva colisiona", func(t *testing.T) {
				_, _, err := store.CreateWithDelivery(ctx, booking("aaaaa", "R2"), delivery("aaaaa", now))
				if !errors.Is(err, model.ErrCodeCollision) {
					t.Errorf("error = %v, want ErrCodeCollision", err)
				}
			})
		})
	}
}

func TestGetByCodeNotFound(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := newStore().GetByCode(context.Background(), "zzzzz"); !errors.Is(err, model.ErrNotFound) {
				t.Errorf("GetByCode() error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestDue(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore()
			now := time.Now().UTC()

			seed := []struct {
				code string
				at   time.Time
			}{
				{"aaaaa", now.Add(-2 * time.Minute)},
				{"bbbbb", now.Add(-time.Minute)},
				{"ccccc", now.Add(time.Hour)}, // futura
			}
			for i, s := range seed {
				if _, _, err := store.CreateWithDelivery(ctx, booking(s.code, string(rune('A'+i))), delivery(s.code, s.at)); err != nil {
					t.Fatalf("seeding %s: %v", s.code, err)
				}
			}

			t.Run("solo las vencidas, las más viejas primero", func(t *testing.T) {
				due, err := store.Due(ctx, now, 0)
				if err != nil {
					t.Fatalf("Due() error = %v", err)
				}
				if len(due) != 2 {
					t.Fatalf("got %d deliveries, want 2", len(due))
				}
				if due[0].Code != "aaaaa" || due[1].Code != "bbbbb" {
					t.Errorf("order = [%s %s], want [aaaaa bbbbb]", due[0].Code, due[1].Code)
				}
			})

			t.Run("respeta el límite", func(t *testing.T) {
				due, err := store.Due(ctx, now, 1)
				if err != nil {
					t.Fatalf("Due() error = %v", err)
				}
				if len(due) != 1 || due[0].Code != "aaaaa" {
					t.Errorf("got %+v, want only aaaaa", due)
				}
			})

			t.Run("excluye las acked", func(t *testing.T) {
				if err := store.MarkAcked(ctx, "aaaaa", now); err != nil {
					t.Fatalf("MarkAcked() error = %v", err)
				}
				due, err := store.Due(ctx, now, 0)
				if err != nil {
					t.Fatalf("Due() error = %v", err)
				}
				if len(due) != 1 || due[0].Code != "bbbbb" {
					t.Errorf("got %+v, want only bbbbb", due)
				}
			})
		})
	}
}

func TestRecordAttempt(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore()
			now := time.Now().UTC()

			if _, _, err := store.CreateWithDelivery(ctx, booking("aaaaa", "R1"), delivery("aaaaa", now)); err != nil {
				t.Fatalf("seeding: %v", err)
			}

			next := now.Add(time.Second)
			if err := store.RecordAttempt(ctx, "aaaaa", next, "connection refused"); err != nil {
				t.Fatalf("RecordAttempt() error = %v", err)
			}

			got, _ := store.Get(ctx, "aaaaa")
			if got.Status != model.DeliverySent {
				t.Errorf("status = %q, want %q", got.Status, model.DeliverySent)
			}
			if got.Attempts != 1 {
				t.Errorf("attempts = %d, want 1", got.Attempts)
			}
			if got.LastError != "connection refused" {
				t.Errorf("lastError = %q, want %q", got.LastError, "connection refused")
			}
			if !got.NextAttemptAt.Equal(next) {
				t.Errorf("nextAttemptAt = %v, want %v", got.NextAttemptAt, next)
			}

			if err := store.RecordAttempt(ctx, "zzzzz", next, ""); !errors.Is(err, model.ErrNotFound) {
				t.Errorf("RecordAttempt(zzzzz) error = %v, want ErrNotFound", err)
			}
		})
	}
}

// acked es terminal: si el ack llega mientras un intento está en vuelo, el
// intento que termina después no debe reabrir la entrega.
func TestRecordAttemptDoesNotClobberAcked(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore()
			now := time.Now().UTC()

			if _, _, err := store.CreateWithDelivery(ctx, booking("aaaaa", "R1"), delivery("aaaaa", now)); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			if err := store.MarkAcked(ctx, "aaaaa", now); err != nil {
				t.Fatalf("MarkAcked() error = %v", err)
			}

			if err := store.RecordAttempt(ctx, "aaaaa", now.Add(time.Hour), "late"); err != nil {
				t.Fatalf("RecordAttempt() error = %v, want nil (no-op)", err)
			}

			got, _ := store.Get(ctx, "aaaaa")
			if got.Status != model.DeliveryAcked {
				t.Errorf("status = %q, want it to stay %q", got.Status, model.DeliveryAcked)
			}
			if got.Attempts != 0 {
				t.Errorf("attempts = %d, want 0", got.Attempts)
			}
			if got.LastError != "" {
				t.Errorf("lastError = %q, want empty", got.LastError)
			}
		})
	}
}

func TestMarkAcked(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newStore()
			now := time.Now().UTC()

			if _, _, err := store.CreateWithDelivery(ctx, booking("aaaaa", "R1"), delivery("aaaaa", now)); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			if err := store.RecordAttempt(ctx, "aaaaa", now, "boom"); err != nil {
				t.Fatalf("RecordAttempt() error = %v", err)
			}

			if err := store.MarkAcked(ctx, "aaaaa", now); err != nil {
				t.Fatalf("MarkAcked() error = %v", err)
			}
			first, _ := store.Get(ctx, "aaaaa")
			if first.AckedAt == nil {
				t.Fatal("ackedAt is nil")
			}
			if first.LastError != "" {
				t.Errorf("lastError = %q, want it cleared", first.LastError)
			}

			// Idempotente: el dispositivo re-ackea los duplicados.
			if err := store.MarkAcked(ctx, "aaaaa", now.Add(time.Hour)); err != nil {
				t.Fatalf("second MarkAcked() error = %v", err)
			}
			second, _ := store.Get(ctx, "aaaaa")
			if !second.AckedAt.Equal(*first.AckedAt) {
				t.Errorf("ackedAt = %v, want it unchanged at %v", second.AckedAt, first.AckedAt)
			}

			if err := store.MarkAcked(ctx, "zzzzz", now); !errors.Is(err, model.ErrNotFound) {
				t.Errorf("MarkAcked(zzzzz) error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestOperationsRespectContext(t *testing.T) {
	for name, newStore := range implementations(t) {
		t.Run(name, func(t *testing.T) {
			store := newStore()
			now := time.Now().UTC()
			if _, _, err := store.CreateWithDelivery(context.Background(), booking("aaaaa", "R1"), delivery("aaaaa", now)); err != nil {
				t.Fatalf("seeding: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			ops := map[string]func() error{
				"CreateWithDelivery": func() error {
					_, _, err := store.CreateWithDelivery(ctx, booking("bbbbb", "R2"), delivery("bbbbb", now))
					return err
				},
				"GetByCode": func() error { _, err := store.GetByCode(ctx, "aaaaa"); return err },
				"Get":       func() error { _, err := store.Get(ctx, "aaaaa"); return err },
				"Due":       func() error { _, err := store.Due(ctx, now, 0); return err },
				"RecordAttempt": func() error {
					return store.RecordAttempt(ctx, "aaaaa", now, "")
				},
				"MarkAcked": func() error { return store.MarkAcked(ctx, "aaaaa", now) },
			}
			for op, run := range ops {
				if err := run(); !errors.Is(err, context.Canceled) {
					t.Errorf("%s() error = %v, want context.Canceled", op, err)
				}
			}
		})
	}
}

// El caso 7 de la consigna: reiniciar el proceso no debe perder las entregas
// pendientes.
func TestFileStoreSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "store.json")
	now := time.Now().UTC()

	first, err := file.New(ctx, path)
	if err != nil {
		t.Fatalf("file.New() error = %v", err)
	}
	if _, _, err := first.CreateWithDelivery(ctx, booking("aaaaa", "R1"), delivery("aaaaa", now)); err != nil {
		t.Fatalf("seeding aaaaa: %v", err)
	}
	if _, _, err := first.CreateWithDelivery(ctx, booking("bbbbb", "R2"), delivery("bbbbb", now)); err != nil {
		t.Fatalf("seeding bbbbb: %v", err)
	}
	if err := first.RecordAttempt(ctx, "aaaaa", now.Add(-time.Second), "offline"); err != nil {
		t.Fatalf("RecordAttempt() error = %v", err)
	}
	if err := first.MarkAcked(ctx, "bbbbb", now); err != nil {
		t.Fatalf("MarkAcked() error = %v", err)
	}

	// "Reinicio": una instancia nueva sobre el mismo archivo.
	second, err := file.New(ctx, path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}

	got, err := second.GetByCode(ctx, "aaaaa")
	if err != nil {
		t.Fatalf("GetByCode() error = %v", err)
	}
	if got.ReservationID != "R1" || got.Guest != "R1@example.com" {
		t.Errorf("booking = %+v, want R1", got)
	}

	pending, err := second.Get(ctx, "aaaaa")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if pending.Status != model.DeliverySent || pending.Attempts != 1 || pending.LastError != "offline" {
		t.Errorf("delivery = %+v, want sent/1/offline", pending)
	}

	acked, _ := second.Get(ctx, "bbbbb")
	if acked.Status != model.DeliveryAcked || acked.AckedAt == nil {
		t.Errorf("delivery = %+v, want acked", acked)
	}

	// La pendiente se retoma sola; la acked no vuelve.
	due, err := second.Due(ctx, now, 0)
	if err != nil {
		t.Fatalf("Due() error = %v", err)
	}
	if len(due) != 1 || due[0].Code != "aaaaa" {
		t.Errorf("due = %+v, want only aaaaa", due)
	}

	// La idempotencia también sobrevive.
	if _, created, err := second.CreateWithDelivery(ctx, booking("ccccc", "R1"), delivery("ccccc", now)); err != nil || created {
		t.Errorf("CreateWithDelivery() = (created=%v, %v), want created=false", created, err)
	}
}

// La escritura es atómica: temporal + rename, sin dejar basura al lado.
func TestFileStoreLeavesNoTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	store, err := file.New(ctx, path)
	if err != nil {
		t.Fatalf("file.New() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		code := string(rune('a'+i)) + "aaaa"
		if _, _, err := store.CreateWithDelivery(ctx, booking(code, code), delivery(code, time.Now().UTC())); err != nil {
			t.Fatalf("CreateWithDelivery() error = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "store.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only store.json", names)
	}
}

// Un snapshot corrupto no debe arrancar en frío y perder entregas en silencio.
func TestFileStoreRejectsCorruptSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := file.New(context.Background(), path); err == nil {
		t.Error("file.New() error = nil, want a failure instead of silently starting empty")
	}
}

func TestFileStoreStartsColdWhenMissing(t *testing.T) {
	store, err := file.New(context.Background(), filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("file.New() error = %v", err)
	}
	if _, err := store.GetByCode(context.Background(), "aaaaa"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("GetByCode() error = %v, want ErrNotFound", err)
	}
}

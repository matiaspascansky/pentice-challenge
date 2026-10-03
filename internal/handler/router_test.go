package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pentice-challenge/internal/handler"
	"pentice-challenge/internal/model"
	"pentice-challenge/internal/repository/memory"
	"pentice-challenge/internal/service"
)

// fixedGen devuelve un código conocido, para poder afirmar que la respuesta
// del POST no lo contiene.
type fixedGen struct {
	mu    sync.Mutex
	codes []string
	calls int
}

func (g *fixedGen) Generate() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.calls++
	if g.calls > len(g.codes) {
		return g.codes[len(g.codes)-1], nil
	}
	return g.codes[g.calls-1], nil
}

type stubNotifier struct{}

func (stubNotifier) Notify(context.Context, model.Notification) error { return nil }

func newTestServer(t *testing.T, codes ...string) http.Handler {
	t.Helper()

	repo := memory.New()
	bookings := service.NewBookingService(repo, &fixedGen{codes: codes})
	deliveries := service.NewDeliveryService(repo, stubNotifier{}, time.Second, time.Minute, time.Second)

	return handler.NewRouter(
		handler.NewBookingHandler(bookings),
		handler.NewDeliveryHandler(deliveries),
	)
}

// do hace un request contra el router y devuelve la respuesta grabada.
func do(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	rec := do(t, newTestServer(t, "a2b34"), http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// La consigna es explícita: el código no viaja en la respuesta del POST.
func TestCreateBookingDoesNotLeakTheCode(t *testing.T) {
	const code = "a2b34"
	router := newTestServer(t, code)

	rec := do(t, router, http.MethodPost, "/bookings", `{"reservationId":"R123","guest":"ana@example.com"}`)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	body := rec.Body.String()
	if strings.Contains(body, code) {
		t.Errorf("response body contains the confirmation code: %s", body)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got["reservationId"] != "R123" || got["status"] != "accepted" {
		t.Errorf("body = %v, want reservationId=R123 and status=accepted", got)
	}
	// Ningún campo extra que pudiera filtrar el código.
	if len(got) != 2 {
		t.Errorf("body has %d fields (%v), want exactly reservationId and status", len(got), got)
	}
	// Y el código sí quedó accesible por el GET.
	if rec := do(t, router, http.MethodGet, "/"+code, ""); rec.Code != http.StatusOK {
		t.Errorf("GET /%s status = %d, want %d", code, rec.Code, http.StatusOK)
	}
}

func TestCreateBookingIsIdempotent(t *testing.T) {
	router := newTestServer(t, "a2b34", "zzzzz")
	const body = `{"reservationId":"R123","guest":"ana@example.com"}`

	for i := 0; i < 2; i++ {
		if rec := do(t, router, http.MethodPost, "/bookings", body); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: status = %d, want %d", i+1, rec.Code, http.StatusAccepted)
		}
	}

	// El segundo código nunca se usó: hay un solo booking.
	if rec := do(t, router, http.MethodGet, "/zzzzz", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET /zzzzz status = %d, want %d: se creó un segundo booking", rec.Code, http.StatusNotFound)
	}
}

func TestCreateBookingRejectsBadInput(t *testing.T) {
	router := newTestServer(t, "a2b34")

	tests := map[string]string{
		"json roto":         `{`,
		"body vacío":        ``,
		"sin reservationId": `{"guest":"ana@example.com"}`,
		"sin guest":         `{"reservationId":"R1"}`,
		"campos en blanco":  `{"reservationId":"  ","guest":"  "}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := do(t, router, http.MethodPost, "/bookings", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestGetBooking(t *testing.T) {
	const code = "a2b34"
	router := newTestServer(t, code)
	do(t, router, http.MethodPost, "/bookings", `{"reservationId":"R123","guest":"ana@example.com"}`)

	t.Run("devuelve la reserva", func(t *testing.T) {
		rec := do(t, router, http.MethodGet, "/"+code, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}

		var got model.Booking
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if got.Code != code || got.ReservationID != "R123" || got.Guest != "ana@example.com" {
			t.Errorf("booking = %+v, want %s/R123/ana@example.com", got, code)
		}
		if got.CreatedAt.IsZero() {
			t.Error("createdAt is zero")
		}
	})

	t.Run("case-insensitive", func(t *testing.T) {
		if rec := do(t, router, http.MethodGet, "/A2B34", ""); rec.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("código inexistente", func(t *testing.T) {
		if rec := do(t, router, http.MethodGet, "/zzzzz", ""); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("formato inválido", func(t *testing.T) {
		for _, path := range []string{"/abc", "/a2b345", "/a2b3!"} {
			if rec := do(t, router, http.MethodGet, path, ""); rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusBadRequest)
			}
		}
	})
}

func TestAck(t *testing.T) {
	const code = "a2b34"
	router := newTestServer(t, code)
	do(t, router, http.MethodPost, "/bookings", `{"reservationId":"R123","guest":"ana@example.com"}`)

	t.Run("ackea y es idempotente", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			rec := do(t, router, http.MethodPost, "/acks", `{"confirmationCode":"`+code+`"}`)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("request %d: status = %d, want %d", i+1, rec.Code, http.StatusNoContent)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("body = %q, want empty", rec.Body.String())
			}
		}
	})

	t.Run("case-insensitive", func(t *testing.T) {
		if rec := do(t, router, http.MethodPost, "/acks", `{"confirmationCode":"A2B34"}`); rec.Code != http.StatusNoContent {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
	})

	t.Run("código inexistente", func(t *testing.T) {
		if rec := do(t, router, http.MethodPost, "/acks", `{"confirmationCode":"zzzzz"}`); rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("entrada inválida", func(t *testing.T) {
		for _, body := range []string{`{`, `{"confirmationCode":"abc"}`, `{}`} {
			if rec := do(t, router, http.MethodPost, "/acks", body); rec.Code != http.StatusBadRequest {
				t.Errorf("body %s: status = %d, want %d", body, rec.Code, http.StatusBadRequest)
			}
		}
	})
}

func TestDeliveryStatus(t *testing.T) {
	const code = "a2b34"
	router := newTestServer(t, code)
	do(t, router, http.MethodPost, "/bookings", `{"reservationId":"R123","guest":"ana@example.com"}`)

	rec := do(t, router, http.MethodGet, "/bookings/"+code+"/delivery", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got model.Delivery
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if got.Status != model.DeliveryPending || got.Attempts != 0 {
		t.Errorf("delivery = %+v, want pending with 0 attempts", got)
	}

	if rec := do(t, router, http.MethodGet, "/bookings/zzzzz/delivery", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// Las rutas fijas tienen que ganarle al wildcard GET /{code}.
func TestFixedRoutesWinOverTheWildcard(t *testing.T) {
	router := newTestServer(t, "healt") // un código de 5 chars parecido a /health

	if rec := do(t, router, http.MethodGet, "/health", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /health status = %d, want %d: el wildcard se comió la ruta fija", rec.Code, http.StatusOK)
	}
}

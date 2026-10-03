package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ackCollector hace de servidor de reservas: junta los acks que manda el
// dispositivo.
type ackCollector struct {
	mu    sync.Mutex
	codes []string
}

func (a *ackCollector) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /acks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"confirmationCode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		a.mu.Lock()
		a.codes = append(a.codes, body.Code)
		a.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (a *ackCollector) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.codes)
}

func newTestDevice(t *testing.T, opts options) (*device, *ackCollector) {
	t.Helper()

	acks := &ackCollector{}
	srv := httptest.NewServer(acks.handler())
	t.Cleanup(srv.Close)

	opts.serverURL = srv.URL
	if opts.ackDelay == 0 {
		opts.ackDelay = time.Millisecond
	}

	d := newDevice(opts)
	t.Cleanup(d.wait)
	return d, acks
}

func notify(t *testing.T, d *device, code, reservationID string) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"confirmationCode":"` + code + `","reservationId":"` + reservationID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
	rec := httptest.NewRecorder()
	d.receive(rec, req)
	return rec
}

func (d *device) displays(code string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	if c, ok := d.seen[code]; ok {
		return c.Displays
	}
	return 0
}

// La garantía de la consigna: aunque la notificación llegue muchas veces, el
// huésped la ve una sola.
func TestDeviceShowsEachConfirmationOnce(t *testing.T) {
	d, acks := newTestDevice(t, options{})

	for i := 0; i < 5; i++ {
		if rec := notify(t, d, "a2b34", "R1"); rec.Code != http.StatusOK {
			t.Fatalf("notification %d: status = %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	d.mu.Lock()
	got := d.seen["a2b34"]
	d.mu.Unlock()

	if got == nil {
		t.Fatal("the confirmation was not recorded")
	}
	if got.Displays != 1 {
		t.Errorf("displays = %d, want 1: el huésped vio la confirmación dos veces", got.Displays)
	}
	if got.Notifications != 5 {
		t.Errorf("notifications = %d, want 5", got.Notifications)
	}
	if got.ReservationID != "R1" {
		t.Errorf("reservationId = %q, want R1", got.ReservationID)
	}

	// Pero re-ackea cada duplicado: si el servidor reenvió es porque el ack
	// anterior no le llegó, y re-ackear es lo que corta el loop.
	d.wait()
	if n := acks.count(); n != 5 {
		t.Errorf("acks sent = %d, want 5 (uno por notificación)", n)
	}
}

// El check y la inserción tienen que ser atómicos: con -race esto detecta
// tanto la carrera como un display duplicado.
func TestDeviceDedupeIsAtomic(t *testing.T) {
	d, _ := newTestDevice(t, options{ackLossRate: 1}) // sin acks, el test mide el dedupe

	const n = 100
	var wg sync.WaitGroup
	firsts := make(chan bool, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			firsts <- d.record(notification{Code: "a2b34", ReservationID: "R1"})
		}()
	}
	wg.Wait()
	close(firsts)

	newCount := 0
	for isNew := range firsts {
		if isNew {
			newCount++
		}
	}
	if newCount != 1 {
		t.Errorf("record() reported a new code %d times, want exactly 1", newCount)
	}
	if got := d.displays("a2b34"); got != 1 {
		t.Errorf("displays = %d, want 1", got)
	}
}

func TestDeviceOfflineRejectsEverything(t *testing.T) {
	d, acks := newTestDevice(t, options{offline: true})

	rec := notify(t, d, "a2b34", "R1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d so the server retries", rec.Code, http.StatusServiceUnavailable)
	}
	if got := d.displays("a2b34"); got != 0 {
		t.Errorf("displays = %d, want 0", got)
	}
	d.wait()
	if n := acks.count(); n != 0 {
		t.Errorf("acks sent = %d, want 0", n)
	}
}

// drop-rate simula el dispositivo que recibe pero no procesa: responde 200 y
// aun así no ackea. Es el caso que muestra que un 200 no es un ack.
func TestDeviceDropRateAnswersOkWithoutProcessing(t *testing.T) {
	d, acks := newTestDevice(t, options{dropRate: 1})

	rec := notify(t, d, "a2b34", "R1")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := d.displays("a2b34"); got != 0 {
		t.Errorf("displays = %d, want 0: la notificación se descartó", got)
	}
	d.wait()
	if n := acks.count(); n != 0 {
		t.Errorf("acks sent = %d, want 0", n)
	}
}

// ack-loss-rate: el huésped ve el código, pero el servidor nunca se entera.
func TestDeviceAckLossShowsTheCodeButSendsNoAck(t *testing.T) {
	d, acks := newTestDevice(t, options{ackLossRate: 1})

	if rec := notify(t, d, "a2b34", "R1"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := d.displays("a2b34"); got != 1 {
		t.Errorf("displays = %d, want 1", got)
	}

	d.wait()
	if n := acks.count(); n != 0 {
		t.Errorf("acks sent = %d, want 0", n)
	}
}

func TestDeviceRejectsBadNotifications(t *testing.T) {
	d, _ := newTestDevice(t, options{})

	tests := map[string]string{
		"json roto":  `{`,
		"sin código": `{"reservationId":"R1"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/notifications", strings.NewReader(body))
			rec := httptest.NewRecorder()
			d.receive(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestValidateRate(t *testing.T) {
	valid := []float64{0, 0.3, 1}
	for _, v := range valid {
		if err := validateRate("drop-rate", v); err != nil {
			t.Errorf("validateRate(%v) error = %v, want nil", v, err)
		}
	}

	invalid := []float64{-0.1, 1.5}
	for _, v := range invalid {
		if err := validateRate("drop-rate", v); err == nil {
			t.Errorf("validateRate(%v) error = nil, want a failure", v)
		}
	}
}

func TestListReportsWhatTheDeviceSaw(t *testing.T) {
	d, _ := newTestDevice(t, options{ackLossRate: 1})
	notify(t, d, "a2b34", "R1")
	notify(t, d, "c5d67", "R2")

	rec := httptest.NewRecorder()
	d.list(rec, httptest.NewRequest(http.MethodGet, "/confirmations", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got []confirmation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d confirmations, want 2", len(got))
	}
	for _, c := range got {
		if c.Displays != 1 {
			t.Errorf("%s: displays = %d, want 1", c.Code, c.Displays)
		}
	}
}

// Flags de falla para la demo: --offline, --drop-rate, --ack-loss-rate.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("guest stopped", "error", err)
		os.Exit(1)
	}
}

type options struct {
	port        string
	serverURL   string
	offline     bool
	dropRate    float64
	ackLossRate float64
	ackDelay    time.Duration
}

func run() error {
	var opts options
	flag.StringVar(&opts.port, "port", "4000", "puerto donde escucha el dispositivo")
	flag.StringVar(&opts.serverURL, "server-url", "http://localhost:3000", "URL base del servidor de reservas")
	flag.BoolVar(&opts.offline, "offline", false, "simula un dispositivo apagado: responde 503 a todo")
	flag.Float64Var(&opts.dropRate, "drop-rate", 0, "fracción de notificaciones que se reciben pero no se procesan (0..1)")
	flag.Float64Var(&opts.ackLossRate, "ack-loss-rate", 0, "fracción de acks que se pierden en el camino (0..1)")
	flag.DurationVar(&opts.ackDelay, "ack-delay", 200*time.Millisecond, "demora del dispositivo antes de ackear")
	flag.Parse()

	if err := validateRate("drop-rate", opts.dropRate); err != nil {
		return err
	}
	if err := validateRate("ack-loss-rate", opts.ackLossRate); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := newDevice(opts)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /notifications", d.receive)
	mux.HandleFunc("GET /confirmations", d.list)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              ":" + opts.port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("guest device listening",
			"port", opts.port,
			"serverUrl", opts.serverURL,
			"offline", opts.offline,
			"dropRate", opts.dropRate,
			"ackLossRate", opts.ackLossRate)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	d.wait() // esperamos los acks en vuelo
	return err
}

func validateRate(name string, v float64) error {
	if v < 0 || v > 1 {
		return fmt.Errorf("--%s must be between 0 and 1, got %v", name, v)
	}
	return nil
}

// confirmation es lo que el dispositivo recuerda de cada código.
type confirmation struct {
	Code          string    `json:"confirmationCode"`
	ReservationID string    `json:"reservationId"`
	DisplayedAt   time.Time `json:"displayedAt"`
	// Notifications cuenta cuántas veces llegó, Displays cuántas veces se le
	// mostró al huésped. Displays debe ser siempre 1: es la garantía.
	Notifications int `json:"notifications"`
	Displays      int `json:"displays"`
	AcksSent      int `json:"acksSent"`
}

type device struct {
	opts options
	http *http.Client

	mu   sync.Mutex
	seen map[string]*confirmation

	acks sync.WaitGroup
}

func newDevice(opts options) *device {
	return &device{
		opts: opts,
		http: &http.Client{Timeout: 3 * time.Second},
		seen: make(map[string]*confirmation),
	}
}

type notification struct {
	Code          string `json:"confirmationCode"`
	ReservationID string `json:"reservationId"`
}

// receive maneja POST /notifications.
func (d *device) receive(w http.ResponseWriter, r *http.Request) {
	if d.opts.offline {
		// Dispositivo apagado: el servidor va a reintentar.
		http.Error(w, "device offline", http.StatusServiceUnavailable)
		return
	}

	var n notification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&n); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if n.Code == "" {
		http.Error(w, "confirmationCode is required", http.StatusBadRequest)
		return
	}

	if d.opts.dropRate > 0 && rand.Float64() < d.opts.dropRate {
		// Respondemos 200 pero no procesamos: así se ve que un 200 no es un
		// ack. El servidor va a reintentar igual, que es lo correcto.
		w.WriteHeader(http.StatusOK)
		slog.Warn("⚠️  notificación descartada sin procesar (drop-rate)", "confirmationCode", n.Code)
		return
	}

	// El ack sale después de responder; el servidor no lo espera en esta
	// respuesta (por eso el canal de ack es asíncrono y separado).
	w.WriteHeader(http.StatusOK)

	if d.record(n) {
		slog.Info("📱 nueva confirmación", "confirmationCode", n.Code, "reservationId", n.ReservationID)
	} else {
		// Acá está el núcleo de "nunca dos veces": ya la vimos, así que no se
		// muestra de nuevo. Pero sí re-ackeamos, porque si el servidor
		// reenvió es porque el ack anterior no le llegó.
		slog.Info("🔁 duplicada: no se muestra, se re-ackea", "confirmationCode", n.Code)
	}

	d.sendAckAsync(n.Code)
}

// record registra la notificación y devuelve true si el código es nuevo.
// El check y la inserción van bajo el mismo lock: dos notificaciones
// simultáneas del mismo código no pueden mostrarse las dos.
func (d *device) record(n notification) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if c, ok := d.seen[n.Code]; ok {
		c.Notifications++
		return false
	}

	d.seen[n.Code] = &confirmation{
		Code:          n.Code,
		ReservationID: n.ReservationID,
		DisplayedAt:   time.Now().UTC(),
		Notifications: 1,
		Displays:      1,
	}
	return true
}

// sendAckAsync ackea en segundo plano, con una pequeña demora que simula al
// dispositivo procesando la notificación.
func (d *device) sendAckAsync(code string) {
	d.acks.Add(1)
	go func() {
		defer d.acks.Done()

		time.Sleep(d.opts.ackDelay)

		if d.opts.ackLossRate > 0 && rand.Float64() < d.opts.ackLossRate {
			// El huésped ya vio el código, pero el servidor no se va a
			// enterar: va a reenviar, y el dispositivo deduplicará.
			slog.Warn("📡 ack perdido en el camino (ack-loss-rate)", "confirmationCode", code)
			return
		}

		if err := d.sendAck(code); err != nil {
			slog.Error("enviando ack", "confirmationCode", code, "error", err)
			return
		}

		d.mu.Lock()
		if c, ok := d.seen[code]; ok {
			c.AcksSent++
		}
		d.mu.Unlock()

		slog.Info("✅ ack enviado", "confirmationCode", code)
	}()
}

func (d *device) sendAck(code string) error {
	body, err := json.Marshal(map[string]string{"confirmationCode": code})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.opts.serverURL+"/acks", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("server responded %d", resp.StatusCode)
	}
	return nil
}

// list maneja GET /confirmations: lo que el dispositivo recibió y mostró.
// No está en la consigna; sirve para verificar que Displays sea siempre 1.
func (d *device) list(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	out := make([]confirmation, 0, len(d.seen))
	for _, c := range d.seen {
		out = append(out, *c)
	}
	d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.Error("writing confirmations", "error", err)
	}
}

func (d *device) wait() {
	d.acks.Wait()
}

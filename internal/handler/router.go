package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter arma el router con los middlewares y las rutas del servicio.
//
// Importante: GET /{code} es un wildcard en la raíz, así que toda ruta fija
// (/health, /bookings, /acks) debe quedar registrada antes.
// Además el service valida el formato ^[a-z0-9]{5}$, con lo cual una ruta
// desconocida más larga nunca se confunde con un código.
func NewRouter(bookings *BookingHandler, deliveries *DeliveryHandler) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(accessLog)

	r.Get("/health", health)
	r.Post("/bookings", bookings.Create)
	r.Post("/acks", deliveries.Ack)
	r.Get("/bookings/{code}/delivery", deliveries.Status)

	// Wildcard al final.
	r.Get("/{code}", bookings.Get)

	return r
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// accessLog loguea cada request con slog, para mantener una sola salida
// estructurada junto con la del worker.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration", time.Since(started).Round(time.Microsecond),
			"requestId", middleware.GetReqID(r.Context()))
	})
}

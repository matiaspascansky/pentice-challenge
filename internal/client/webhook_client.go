// Package client contiene los clientes HTTP hacia servicios externos.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"pentice-challenge/internal/model"
)

// maxErrorBodyBytes es cuánto del cuerpo de una respuesta de error leemos para
// el log. No queremos cargar en memoria lo que devuelva un guest roto.
const maxErrorBodyBytes = 512

// WebhookNotifier entrega las notificaciones por HTTP al dispositivo del
// huésped. Implementa service.Notifier.
type WebhookNotifier struct {
	url  string
	http *http.Client
}

// NewWebhookNotifier arma el cliente apuntando a la URL del webhook.
// El timeout por request lo impone el contexto que pasa el service, así que el
// http.Client no lleva uno propio.
func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		url:  url,
		http: &http.Client{},
	}
}

// Notify hace POST del payload al webhook. Cualquier status fuera de 2xx es un
// error: el intento falló y el worker lo reintentará.
//
// Un 2xx tampoco significa que el huésped vio el código; solo que el request
// llegó. El ack viaja por separado.
func (n *WebhookNotifier) Notify(ctx context.Context, notification model.Notification) error {
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("webhook responded %d: %s", resp.StatusCode, bytes.TrimSpace(detail))
	}

	// Drenamos el cuerpo para que la conexión se pueda reutilizar.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
	return nil
}

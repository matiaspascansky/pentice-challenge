package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody es el cuerpo uniforme de los errores de la API: todos los errores
// del servicio salen con esta forma, incluidos los 404 y 405 que antes
// resolvían los handlers por defecto del router con texto plano.
type errorBody struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// writeJSON serializa v como JSON con el status dado. Si la escritura falla ya
// se enviaron los headers, así que solo lo logueamos.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writing json response", "error", err)
	}
}

// writeError responde con el status dado y un mensaje apto para el cliente.
// El status viaja también en el cuerpo para que un cliente que solo mira el
// JSON no necesite leer la línea de estado.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Status: status, Message: msg})
}

package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody es el cuerpo uniforme de los errores de la API.
type errorBody struct {
	Error string `json:"error"`
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
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// writeJSON sends one object. It marshals into memory first so a failure
// half way through does not leave a broken body behind a 200.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Default().Error("the response could not be turned into JSON", "err", err)
		fail(w, http.StatusInternalServerError, "the answer could not be prepared. Try again.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b)
}

// fail sends an error. The message says what happened and what to do, in
// plain English, and never names a file on disk.
func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

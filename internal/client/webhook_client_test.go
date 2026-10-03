package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pentice-challenge/internal/client"
	"pentice-challenge/internal/model"
)

func TestNotifySendsThePayload(t *testing.T) {
	var (
		gotPath        string
		gotContentType string
		gotBody        model.Notification
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := client.NewWebhookNotifier(srv.URL + "/notifications")
	want := model.Notification{Code: "a2b34", ReservationID: "R123"}

	if err := notifier.Notify(context.Background(), want); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	if gotPath != "/notifications" {
		t.Errorf("path = %q, want %q", gotPath, "/notifications")
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if gotBody != want {
		t.Errorf("payload = %+v, want %+v", gotBody, want)
	}
}

func TestNotifyTreatsNon2xxAsFailure(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusInternalServerError, http.StatusNotFound, http.StatusMovedPermanently} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "device offline", status)
		}))

		err := client.NewWebhookNotifier(srv.URL).Notify(context.Background(), model.Notification{Code: "a2b34"})
		srv.Close()

		if err == nil {
			t.Errorf("status %d: Notify() error = nil, want a failure so the worker retries", status)
			continue
		}
		// El error tiene que servir para el log: status y cuerpo.
		if !strings.Contains(err.Error(), "device offline") {
			t.Errorf("status %d: error = %q, want it to include the response body", status, err)
		}
	}
}

func TestNotifyRespectsContextTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // dispositivo colgado
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := client.NewWebhookNotifier(srv.URL).Notify(ctx, model.Notification{Code: "a2b34"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Notify() error = nil, want a timeout")
	}
	if elapsed > time.Second {
		t.Errorf("Notify() took %v, want it to give up with the context", elapsed)
	}
}

func TestNotifyFailsWhenTheDeviceIsUnreachable(t *testing.T) {
	// Puerto cerrado: el caso "dispositivo apagado".
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if err := client.NewWebhookNotifier(url).Notify(context.Background(), model.Notification{Code: "a2b34"}); err == nil {
		t.Error("Notify() error = nil, want a connection failure")
	}
}

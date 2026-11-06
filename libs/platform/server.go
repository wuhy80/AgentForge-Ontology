package platform

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ProtectRoutes leaves only probes and independently authenticated internal
// endpoints outside user authentication. Service credentials can read current
// metadata but cannot edit definitions or call user data endpoints.
func ProtectRoutes(secret, serviceToken string, metadata bool, app http.Handler) http.Handler {
	public := Authenticate(secret, app)
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", app)
	mux.Handle("GET /readyz", app)
	mux.Handle("/internal/", app)
	if metadata {
		serviceRead := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if EqualSecret(Bearer(r), serviceToken) {
				app.ServeHTTP(w, r)
				return
			}
			public.ServeHTTP(w, r)
		})
		mux.Handle("GET /metadata/definitions/{id}", serviceRead)
		mux.Handle("GET /metadata/resolve", serviceRead)
	}
	mux.Handle("/", public)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func Serve(ctx context.Context, address string, handler http.Handler) error {
	s := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.Shutdown(stop); err != nil {
			_ = s.Close()
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

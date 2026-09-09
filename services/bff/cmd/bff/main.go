// Servidor del canal del titular (BFF), incluido el lado humano del modelo B.
//
// Existe por el sandbox. El modelo B necesita DOS canales vivos —la plataforma
// pide el permiso por la API de socio y una persona lo concede aquí— y sin este
// binario el único sitio donde el flujo completo corría era una prueba de Go.
// Un socio no puede integrar contra una prueba.
//
// Variables de entorno:
//
//	DATABASE_URL    PostgreSQL. Habilita las pantallas de permiso del modelo B.
//	CORE_ADDR       core gRPC (por defecto 127.0.0.1:50051)
//	BIND_ADDR       dirección de escucha (por defecto 127.0.0.1:8080)
//	ALLOW_DEV_AUTH  "true" monta la autenticación de juguete y /dev. SOLO sandbox.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/bff"
	"github.com/aibank/aibank/services/bff/sim"
	"github.com/aibank/aibank/services/oauth"
	_ "github.com/lib/pq"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("el servicio terminó con error", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// La autenticación real son passkeys FIDO2 vinculadas al dispositivo, y no
	// están construidas. Hasta que lo estén este binario SOLO puede arrancar en
	// modo sandbox, y hay que pedirlo explícitamente: un servicio que se queda
	// con el autenticador de juguete porque nadie configuró el de verdad es
	// exactamente el accidente que este error impide.
	if os.Getenv("ALLOW_DEV_AUTH") != "true" {
		return errors.New(
			"este binario solo sabe autenticar con el sustituto de desarrollo; " +
				"arráncalo con ALLOW_DEV_AUTH=true y NUNCA contra datos reales")
	}
	log.Warn("AUTENTICACIÓN DE DESARROLLO ACTIVA: cualquiera puede emitir una sesión")

	core, err := coreclient.Dial(ctx, env("CORE_ADDR", "127.0.0.1:50051"))
	if err != nil {
		return err
	}
	defer core.Close()

	auth := sim.NewAuthenticator()
	server := bff.NewServer(core, auth, log)

	// Sin base de datos el canal funciona igual, solo que sin permisos delegados.
	// Se avisa, porque arrancar callado sin la mitad del modelo B convierte un
	// 404 en un misterio para quien integra.
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return err
		}
		defer db.Close()

		store := oauth.NewStore(db)
		if err := store.Migrate(ctx); err != nil {
			return err
		}
		server = server.WithConsents(store)
	} else {
		log.Warn("sin DATABASE_URL: el canal arranca sin las pantallas de permiso del modelo B")
	}

	mux := http.NewServeMux()
	mux.Handle("/", server.Handler())
	mountDevRoutes(mux, core, auth, log)

	addr := env("BIND_ADDR", "127.0.0.1:8080")
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("canal del titular escuchando", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

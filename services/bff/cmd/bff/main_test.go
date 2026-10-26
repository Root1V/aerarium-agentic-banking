package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sin una forma de autenticar el canal no arranca. Las passkeys son la forma
// real; el sustituto de desarrollo hay que pedirlo explícitamente. Es lo que
// impide que el canal acabe sirviendo datos reales con una autenticación de
// juguete porque nadie configuró la de verdad.
func TestNoArrancaSinPedirLaAutenticacionDeDesarrollo(t *testing.T) {
	t.Setenv("ALLOW_DEV_AUTH", "")
	t.Setenv("PASSKEY_RP_ID", "")

	err := run(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("arrancó sin ALLOW_DEV_AUTH")
	}
	if !strings.Contains(err.Error(), "ALLOW_DEV_AUTH") {
		t.Fatalf("el error no dice cómo arrancarlo: %v", err)
	}
}

// Un valor cualquiera tampoco vale: la variable es una afirmación, no una
// casilla que se marca sola con cualquier cosa que haya en el entorno.
func TestSoloElValorExactoHabilitaElModoDesarrollo(t *testing.T) {
	t.Setenv("ALLOW_DEV_AUTH", "1")
	t.Setenv("PASSKEY_RP_ID", "")

	if err := run(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("arrancó con ALLOW_DEV_AUTH=1")
	}
}

// Las passkeys y las sesiones viven en la base: configurarlas sin ella sería un
// canal que acepta ceremonias y no puede recordar ninguna.
func TestLasPasskeysExigenBaseDeDatos(t *testing.T) {
	t.Setenv("ALLOW_DEV_AUTH", "")
	t.Setenv("PASSKEY_RP_ID", "aerarium.test")
	t.Setenv("DATABASE_URL", "")

	err := run(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("arranque = %v; se esperaba que pidiera DATABASE_URL", err)
	}
}

// El simulador de titulares abre cuentas y emite sesiones. En un entorno alojado
// eso tiene que estar detrás de algo, y lo que lo cierra es DEV_API_KEY.
func TestElSimuladorSeCierraConClave(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	llamado := false
	protegida := devGate(func(http.ResponseWriter, *http.Request) { llamado = true }, "secreta", log)

	casos := []struct {
		nombre   string
		clave    string
		esperado int
	}{
		{"sin clave", "", http.StatusUnauthorized},
		{"clave equivocada", "otra", http.StatusUnauthorized},
		{"clave correcta", "secreta", http.StatusOK},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			llamado = false
			req := httptest.NewRequest("POST", "/dev/holders", nil)
			if caso.clave != "" {
				req.Header.Set("X-Dev-Key", caso.clave)
			}
			rec := httptest.NewRecorder()
			protegida(rec, req)

			if rec.Code != caso.esperado {
				t.Fatalf("estado %d, se esperaba %d", rec.Code, caso.esperado)
			}
			if llamado != (caso.esperado == http.StatusOK) {
				t.Fatalf("el handler se ejecutó=%v con %q", llamado, caso.nombre)
			}
		})
	}
}

// Sin clave configurada la puerta no existe: es el caso del portátil, donde
// exigirla solo sería fricción.
func TestSinClaveConfiguradaElSimuladorQuedaAbierto(t *testing.T) {
	llamado := false
	abierta := devGate(func(http.ResponseWriter, *http.Request) { llamado = true }, "",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	abierta(httptest.NewRecorder(), httptest.NewRequest("POST", "/dev/holders", nil))
	if !llamado {
		t.Fatal("la puerta bloqueó sin haber clave configurada")
	}
}

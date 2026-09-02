package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// El canal del titular solo sabe autenticar con el sustituto de desarrollo: las
// passkeys todavía no existen. Que el binario se niegue a arrancar sin pedirlo
// explícitamente es lo único que impide que acabe sirviendo datos reales con una
// autenticación de juguete, y por eso se prueba.
func TestNoArrancaSinPedirLaAutenticacionDeDesarrollo(t *testing.T) {
	t.Setenv("ALLOW_DEV_AUTH", "")

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

	if err := run(slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("arrancó con ALLOW_DEV_AUTH=1")
	}
}

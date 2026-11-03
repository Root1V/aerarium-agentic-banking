package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/aibank/aibank/adapters/kyc/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/aibank/aibank/services/bff"
	"github.com/aibank/aibank/services/onboarding"
)

// El alta en el sandbox.
//
// La máquina de estados es la real; lo simulado es el proveedor de identidad y
// de listas. Para que cada desenlace se pueda ensayar sin inventar casos, hay
// DNI de prueba fijos —como las tarjetas de prueba de una pasarela de pago—. Se
// documentan en SANDBOX.md.
//
// En producción esto lo reemplaza el adaptador del proveedor real (RENIEC vía
// Truora, Metamap o similar), que es quien decide de verdad.

// DNI de prueba.
const (
	TestDNIRejected     = "00000000" // identidad no superada → rechazo
	TestDNIInconclusive = "11111111" // verificación no concluyente → revisión
	TestDNIWatchlist    = "22222222" // coincidencia en listas → revisión
)

func setupOnboarding(ctx context.Context, db *sql.DB, core *coreclient.Client, currency string) (*onboarding.Service, *onboarding.Store, string, error) {
	store := onboarding.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return nil, nil, "", fmt.Errorf("migrar el esquema del alta: %w", err)
	}

	// El producto de nivel 1 de la moneda del alta (doc 14): topes bajos, que es
	// lo que permite abrirla sin prueba de vida. Los topes viven en bff.
	product, err := bff.EnsureBasicProduct(ctx, core, currency)
	if err != nil {
		return nil, nil, "", fmt.Errorf("producto de nivel 1: %w", err)
	}

	verifier := sim.NewIdentityVerifier()
	verifier.SetOutcome(TestDNIRejected, sim.Rejected("prueba de vida no superada"))
	verifier.SetOutcome(TestDNIInconclusive, sim.Inconclusive("imagen del documento ilegible"))

	screener := sim.NewSanctionsScreener()
	screener.AddToWatchlist(TestDNIWatchlist, "OFAC-SDN")

	return onboarding.NewService(store, core, verifier, screener), store, product, nil
}

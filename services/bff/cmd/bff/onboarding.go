package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aibank/aibank/adapters/kyc/sim"
	"github.com/aibank/aibank/clients/go/coreclient"
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

// Producto de las cuentas de nivel 1 (doc 14).
//
// Los topes son PROVISIONALES: los fija la norma de cuentas simplificadas de la
// SBS, que hay que validar con el asesor regulatorio. Lo que sí es definitivo es
// que el nivel 1 tiene topes bajos — es lo que permite abrirla sin prueba de vida.
const (
	basicProductPrefix = "BASICA-"
	basicMaxBalance    = 1_000 * 1_000_000 // 1.000 USD
	basicMaxTx         = 300 * 1_000_000   //   300 USD
)

func setupOnboarding(ctx context.Context, db *sql.DB, core *coreclient.Client, currency string) (*onboarding.Service, *onboarding.Store, string, error) {
	store := onboarding.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		return nil, nil, "", fmt.Errorf("migrar el esquema del alta: %w", err)
	}

	product := basicProductPrefix + currency
	maxBalance, maxTx := int64(basicMaxBalance), int64(basicMaxTx)
	if _, err := core.CreateProduct(ctx, coreclient.NewProduct{
		Code:                 product,
		Name:                 "Cuenta básica " + currency,
		Currency:             currency,
		MaxBalanceMicros:     &maxBalance,
		MaxTransactionMicros: &maxTx,
	}); err != nil && !errors.Is(err, coreclient.ErrAlreadyExists) {
		return nil, nil, "", fmt.Errorf("producto de nivel 1: %w", err)
	}

	verifier := sim.NewIdentityVerifier()
	verifier.SetOutcome(TestDNIRejected, sim.Rejected("prueba de vida no superada"))
	verifier.SetOutcome(TestDNIInconclusive, sim.Inconclusive("imagen del documento ilegible"))

	screener := sim.NewSanctionsScreener()
	screener.AddToWatchlist(TestDNIWatchlist, "OFAC-SDN")

	return onboarding.NewService(store, core, verifier, screener), store, product, nil
}

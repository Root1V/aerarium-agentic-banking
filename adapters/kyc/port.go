// Package kyc define el PUERTO de verificación de identidad y screening AML.
//
// Es un proceso REGULADO: los estándares GAFI/FATF obligan a identificar al
// cliente antes de abrirle una cuenta, y a poder demostrarle al supervisor por qué
// se aprobó a cada persona. Por eso cada verificación devuelve, además del
// veredicto, la referencia del proveedor y el detalle que sustenta la decisión.
//
// En la región los proveedores cambian por país (Incode/Metamap, Truora, Unico en
// Brasil) porque cada uno integra fuentes gubernamentales distintas: CURP e INE en
// México, CPF en Brasil, DNI en Perú, cédula en Colombia. Esta interfaz permite
// cambiarlos sin tocar el flujo de alta.
package kyc

import (
	"context"
	"errors"
	"time"
)

// ErrProviderUnavailable: el proveedor no respondió. Reintentable — y el reintento
// NO debe volver a cobrar una verificación ya realizada.
var ErrProviderUnavailable = errors.New("proveedor de KYC no disponible")

// Applicant son los datos mínimos de identificación.
//
// Contiene PII: no debe registrarse en logs ni viajar a sistemas que no la
// necesiten. El flujo de alta guarda referencias, no copias del documento.
type Applicant struct {
	FullName       string
	DocumentType   string // "INE", "CPF", "DNI", "CEDULA", "PASSPORT"
	DocumentNumber string
	DateOfBirth    time.Time
	CountryCode    string // ISO-3166 alfa-2
}

// IdentityOutcome es el veredicto de la verificación de identidad.
type IdentityOutcome int

const (
	// IdentityVerified: documento auténtico, prueba de vida superada y coincidencia
	// con la fuente oficial.
	IdentityVerified IdentityOutcome = iota
	// IdentityRejected: el documento o la biometría no pasaron.
	IdentityRejected
	// IdentityInconclusive: hace falta revisión humana (foto de mala calidad,
	// fuente oficial sin respuesta).
	IdentityInconclusive
)

func (o IdentityOutcome) String() string {
	switch o {
	case IdentityVerified:
		return "verified"
	case IdentityRejected:
		return "rejected"
	default:
		return "inconclusive"
	}
}

// IdentityResult es la respuesta del proveedor de identidad.
type IdentityResult struct {
	Outcome IdentityOutcome
	// ProviderRef permite reconstruir la evidencia ante el supervisor.
	ProviderRef string
	// LivenessPassed distingue una prueba de vida superada de una simple lectura
	// de documento. Es obligatoria, no opcional: buena parte del fraude de cuenta
	// nueva se hace hoy con identidades sintéticas generadas por IA, que superan
	// una validación documental pero no una prueba de vida.
	LivenessPassed bool
	// Detail sustenta la decisión. No debe contener PII innecesaria.
	Detail string
}

// IdentityVerifier verifica documento + biometría contra fuentes oficiales.
type IdentityVerifier interface {
	Name() string
	// Verify DEBE ser idempotente por reference: un reintento tras un timeout no
	// puede volver a cobrar la verificación.
	Verify(ctx context.Context, reference string, applicant Applicant) (*IdentityResult, error)
}

// ScreeningOutcome es el veredicto del screening de listas.
type ScreeningOutcome int

const (
	// ScreeningClear: sin coincidencias.
	ScreeningClear ScreeningOutcome = iota
	// ScreeningHit: coincidencia con una lista de sanciones o PEP. NO implica
	// rechazo automático: los falsos positivos por homonimia son frecuentes y la
	// decisión corresponde a un analista de cumplimiento.
	ScreeningHit
)

func (o ScreeningOutcome) String() string {
	if o == ScreeningHit {
		return "hit"
	}
	return "clear"
}

// ScreeningResult es la respuesta del screening.
type ScreeningResult struct {
	Outcome     ScreeningOutcome
	ProviderRef string
	// Lists identifica las listas donde hubo coincidencia (OFAC, ONU, PEP local).
	Lists  []string
	Detail string
}

// SanctionsScreener contrasta al solicitante con listas de sanciones y PEP.
type SanctionsScreener interface {
	Name() string
	Screen(ctx context.Context, reference string, applicant Applicant) (*ScreeningResult, error)
}

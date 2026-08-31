// Package sim simula los proveedores de KYC y screening.
//
// Determinista por diseño: los desenlaces se programan por número de documento,
// no al azar. Un test de cumplimiento que falla debe poder reproducirse tal cual.
//
// Cuenta además las llamadas efectivas, lo que permite verificar una propiedad
// con impacto directo en costos: reanudar un alta interrumpida NO debe volver a
// pedir (ni pagar) una verificación ya realizada.
package sim

import (
	"context"
	"fmt"
	"sync"

	"github.com/aibank/aibank/adapters/kyc"
	"github.com/google/uuid"
)

// IdentityVerifier es un verificador de identidad simulado.
type IdentityVerifier struct {
	mu sync.Mutex
	// outcomes fija el veredicto por número de documento; el resto se aprueba.
	outcomes map[string]kyc.IdentityResult
	// cache reproduce la idempotencia del proveedor: la misma referencia devuelve
	// el mismo resultado sin volver a verificar.
	cache    map[string]kyc.IdentityResult
	failNext int
	calls    int
}

func NewIdentityVerifier() *IdentityVerifier {
	return &IdentityVerifier{
		outcomes: make(map[string]kyc.IdentityResult),
		cache:    make(map[string]kyc.IdentityResult),
	}
}

func (v *IdentityVerifier) Name() string { return "sim-identity" }

// SetOutcome fija el veredicto para un documento concreto.
func (v *IdentityVerifier) SetOutcome(documentNumber string, result kyc.IdentityResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.outcomes[documentNumber] = result
}

// FailNext hace que las próximas n llamadas respondan ErrProviderUnavailable.
func (v *IdentityVerifier) FailNext(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.failNext = n
}

// Calls informa cuántas verificaciones se cobraron efectivamente.
func (v *IdentityVerifier) Calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

func (v *IdentityVerifier) Verify(_ context.Context, reference string, applicant kyc.Applicant) (*kyc.IdentityResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if cached, ok := v.cache[reference]; ok {
		return &cached, nil
	}
	if v.failNext > 0 {
		v.failNext--
		return nil, fmt.Errorf("%w: timeout", kyc.ErrProviderUnavailable)
	}

	v.calls++
	result, ok := v.outcomes[applicant.DocumentNumber]
	if !ok {
		result = kyc.IdentityResult{
			Outcome:        kyc.IdentityVerified,
			LivenessPassed: true,
			Detail:         "documento válido y prueba de vida superada",
		}
	}
	result.ProviderRef = "idv-" + uuid.NewString()
	v.cache[reference] = result
	return &result, nil
}

// Rejected construye un veredicto de rechazo por prueba de vida.
func Rejected(detail string) kyc.IdentityResult {
	return kyc.IdentityResult{Outcome: kyc.IdentityRejected, LivenessPassed: false, Detail: detail}
}

// Inconclusive construye un veredicto que exige revisión humana.
func Inconclusive(detail string) kyc.IdentityResult {
	return kyc.IdentityResult{Outcome: kyc.IdentityInconclusive, LivenessPassed: true, Detail: detail}
}

// ---------------------------------------------------------------- screening

// SanctionsScreener es un screening de listas simulado.
type SanctionsScreener struct {
	mu        sync.Mutex
	watchlist map[string][]string // documento -> listas donde aparece
	cache     map[string]kyc.ScreeningResult
	failNext  int
	calls     int
}

func NewSanctionsScreener() *SanctionsScreener {
	return &SanctionsScreener{
		watchlist: make(map[string][]string),
		cache:     make(map[string]kyc.ScreeningResult),
	}
}

func (s *SanctionsScreener) Name() string { return "sim-screening" }

// AddToWatchlist marca un documento como coincidente con las listas indicadas.
func (s *SanctionsScreener) AddToWatchlist(documentNumber string, lists ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchlist[documentNumber] = lists
}

func (s *SanctionsScreener) FailNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = n
}

func (s *SanctionsScreener) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *SanctionsScreener) Screen(_ context.Context, reference string, applicant kyc.Applicant) (*kyc.ScreeningResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached, ok := s.cache[reference]; ok {
		return &cached, nil
	}
	if s.failNext > 0 {
		s.failNext--
		return nil, fmt.Errorf("%w: timeout", kyc.ErrProviderUnavailable)
	}

	s.calls++
	result := kyc.ScreeningResult{
		Outcome:     kyc.ScreeningClear,
		ProviderRef: "scr-" + uuid.NewString(),
		Detail:      "sin coincidencias",
	}
	if lists, hit := s.watchlist[applicant.DocumentNumber]; hit {
		result.Outcome = kyc.ScreeningHit
		result.Lists = lists
		result.Detail = "coincidencia por nombre y documento"
	}
	s.cache[reference] = result
	return &result, nil
}

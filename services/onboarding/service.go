// Package onboarding orquesta el alta de clientes.
//
// El flujo es una máquina de estados PERSISTIDA y REANUDABLE: cada paso se
// registra al completarse, de modo que una caída a mitad del alta se retoma donde
// quedó. Eso importa por tres razones concretas:
//
//   - Cada verificación de identidad se paga al proveedor. Repetirla es costo puro.
//   - Volver a pedirle documentos al cliente hunde la conversión del alta, que es
//     el embudo más caro de un neobanco.
//   - El supervisor puede exigir el sustento de cada aprobación: los pasos y sus
//     resultados SON ese rastro de auditoría.
package onboarding

import (
	"context"
	"errors"
	"fmt"

	"github.com/aibank/aibank/adapters/kyc"
	"github.com/aibank/aibank/clients/go/coreclient"
	"github.com/google/uuid"
)

// Service orquesta el alta.
type Service struct {
	store    *Store
	core     *coreclient.Client
	verifier kyc.IdentityVerifier
	screener kyc.SanctionsScreener
}

func NewService(store *Store, core *coreclient.Client, verifier kyc.IdentityVerifier, screener kyc.SanctionsScreener) *Service {
	return &Service{store: store, core: core, verifier: verifier, screener: screener}
}

// Submit registra una solicitud y avanza el alta hasta donde pueda.
//
// Reenviar la misma ExternalRef no abre un expediente nuevo: retoma el existente.
func (s *Service) Submit(ctx context.Context, req SubmitRequest) (*Application, error) {
	if req.ExternalRef == "" {
		return nil, errors.New("ExternalRef es obligatorio: sin él no hay idempotencia")
	}
	if req.ProductCode == "" || req.AccountCode == "" {
		return nil, errors.New("ProductCode y AccountCode son obligatorios")
	}

	app, err := s.store.Create(ctx, &Application{
		ID:          uuid.NewString(),
		ExternalRef: req.ExternalRef,
		CustomerID:  req.CustomerID,
		ProductCode: req.ProductCode,
		Status:      StatusInProgress,
		Applicant:   req.Applicant,
	})
	if err != nil {
		return nil, err
	}
	return s.advance(ctx, app, req.AccountCode)
}

// Resume retoma un expediente interrumpido sin repetir pasos ya completados.
func (s *Service) Resume(ctx context.Context, applicationID, accountCode string) (*Application, error) {
	app, err := s.store.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	return s.advance(ctx, app, accountCode)
}

// ApproveManually resuelve una revisión humana. Es la decisión del analista de
// cumplimiento sobre una coincidencia de listas, y queda registrada como tal.
func (s *Service) ApproveManually(ctx context.Context, applicationID, reviewer, accountCode string) (*Application, error) {
	app, err := s.store.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if app.Status != StatusManualReview {
		return nil, fmt.Errorf("el expediente no está en revisión manual (estado: %s)", app.Status)
	}

	if err := s.store.RecordStep(ctx, app.ID, StepRecord{
		Step:     StepManualReview,
		Outcome:  "approved",
		Provider: reviewer,
		Detail:   "coincidencia de listas descartada por análisis humano",
	}); err != nil {
		return nil, err
	}
	if err := s.store.UpdateStatus(ctx, app.ID, StatusInProgress, ""); err != nil {
		return nil, err
	}

	app.Status = StatusInProgress
	return s.advance(ctx, app, accountCode)
}

// RejectManually cierra el expediente por decisión de cumplimiento.
func (s *Service) RejectManually(ctx context.Context, applicationID, reviewer, reason string) (*Application, error) {
	app, err := s.store.Get(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if err := s.store.RecordStep(ctx, app.ID, StepRecord{
		Step: StepManualReview, Outcome: "rejected", Provider: reviewer, Detail: reason,
	}); err != nil {
		return nil, err
	}
	if err := s.store.UpdateStatus(ctx, app.ID, StatusRejected, reason); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, app.ID)
}

// AuditTrail devuelve la evidencia del expediente, en orden.
func (s *Service) AuditTrail(ctx context.Context, applicationID string) ([]StepRecord, error) {
	return s.store.Steps(ctx, applicationID)
}

// ---------------------------------------------------------------- motor

// advance ejecuta los pasos pendientes en orden y se detiene ante un estado final
// o ante un paso que no se puede completar todavía.
func (s *Service) advance(ctx context.Context, app *Application, accountCode string) (*Application, error) {
	if app.Status == StatusApproved || app.Status == StatusRejected {
		return app, nil
	}

	done, err := s.store.completedSteps(ctx, app.ID)
	if err != nil {
		return nil, err
	}

	for _, step := range pipeline {
		if _, already := done[step]; already {
			continue
		}

		stop, err := s.runStep(ctx, app, step, done, accountCode)
		if err != nil {
			// El expediente queda como está: reanudarlo retomará este mismo paso.
			return nil, fmt.Errorf("paso %s: %w", step, err)
		}
		if stop {
			break
		}

		refreshed, err := s.store.completedSteps(ctx, app.ID)
		if err != nil {
			return nil, err
		}
		done = refreshed
	}

	return s.store.Get(ctx, app.ID)
}

// runStep ejecuta un paso. Devuelve stop=true cuando el alta no debe continuar
// (rechazo o revisión manual).
func (s *Service) runStep(ctx context.Context, app *Application, step Step, done map[Step]StepRecord, accountCode string) (bool, error) {
	switch step {
	case StepIdentityVerification:
		result, err := s.verifier.Verify(ctx, s.providerRef(app, step), applicantFrom(app))
		if err != nil {
			return true, err
		}
		if recErr := s.store.RecordStep(ctx, app.ID, StepRecord{
			Step: step, Outcome: result.Outcome.String(), Provider: s.verifier.Name(),
			ProviderRef: result.ProviderRef, Detail: result.Detail,
		}); recErr != nil {
			return true, recErr
		}
		return false, nil

	case StepSanctionsScreening:
		result, err := s.screener.Screen(ctx, s.providerRef(app, step), applicantFrom(app))
		if err != nil {
			return true, err
		}
		detail := result.Detail
		if result.Outcome == kyc.ScreeningHit {
			detail = fmt.Sprintf("%s (listas: %v)", detail, result.Lists)
		}
		if recErr := s.store.RecordStep(ctx, app.ID, StepRecord{
			Step: step, Outcome: result.Outcome.String(), Provider: s.screener.Name(),
			ProviderRef: result.ProviderRef, Detail: detail,
		}); recErr != nil {
			return true, recErr
		}
		return false, nil

	case StepDecision:
		return s.decide(ctx, app, done)

	case StepAccountOpening:
		accountID, err := s.openAccount(ctx, app, accountCode)
		if err != nil {
			return true, err
		}
		if recErr := s.store.RecordStep(ctx, app.ID, StepRecord{
			Step: step, Outcome: "opened", Provider: "core", ProviderRef: accountID,
		}); recErr != nil {
			return true, recErr
		}
		if err := s.store.SetAccount(ctx, app.ID, accountID); err != nil {
			return true, err
		}
		app.AccountID = accountID
		app.Status = StatusApproved
		return false, nil
	}

	return true, fmt.Errorf("paso desconocido: %s", step)
}

// decide combina los resultados previos.
//
// Criterios, en este orden:
//  1. Identidad rechazada o prueba de vida no superada -> rechazo. No es negociable:
//     buena parte del fraude de cuenta nueva usa identidades sintéticas.
//  2. Identidad no concluyente -> revisión humana.
//  3. Coincidencia en listas -> revisión humana, NO rechazo automático. Los falsos
//     positivos por homonimia son frecuentes y rechazar a una persona por su nombre
//     sin análisis es tanto un problema regulatorio como de trato al cliente.
func (s *Service) decide(ctx context.Context, app *Application, done map[Step]StepRecord) (bool, error) {
	identity := done[StepIdentityVerification]
	screening := done[StepSanctionsScreening]

	switch {
	case identity.Outcome == kyc.IdentityRejected.String():
		reason := "verificación de identidad no superada"
		if err := s.finish(ctx, app, StepDecision, "rejected", reason, StatusRejected); err != nil {
			return true, err
		}
		return true, nil

	case identity.Outcome == kyc.IdentityInconclusive.String():
		return true, s.finish(ctx, app, StepDecision, "manual_review",
			"verificación de identidad no concluyente", StatusManualReview)

	case screening.Outcome == kyc.ScreeningHit.String():
		return true, s.finish(ctx, app, StepDecision, "manual_review",
			"coincidencia en listas: requiere análisis de cumplimiento", StatusManualReview)

	default:
		if err := s.store.RecordStep(ctx, app.ID, StepRecord{
			Step: StepDecision, Outcome: "approved", Provider: "policy",
			Detail: "identidad verificada y screening sin coincidencias",
		}); err != nil {
			return true, err
		}
		return false, nil
	}
}

// finish registra el paso de decisión y deja el expediente en un estado que
// requiere intervención (rechazo o revisión).
func (s *Service) finish(ctx context.Context, app *Application, step Step, outcome, reason string, status Status) error {
	if err := s.store.RecordStep(ctx, app.ID, StepRecord{
		Step: step, Outcome: outcome, Provider: "policy", Detail: reason,
	}); err != nil {
		return err
	}
	if err := s.store.UpdateStatus(ctx, app.ID, status, reason); err != nil {
		return err
	}
	app.Status = status
	return nil
}

// openAccount abre la cuenta en el core.
//
// Si la cuenta ya existe con ese código, se reutiliza: un reintento tras una caída
// entre abrir la cuenta y registrar el paso no debe dejar al cliente con dos cuentas.
func (s *Service) openAccount(ctx context.Context, app *Application, accountCode string) (string, error) {
	account, err := s.core.OpenCustomerAccount(ctx, accountCode, app.Applicant.FullName, app.CustomerID, app.ProductCode)
	switch {
	case err == nil:
		return account.Id, nil
	case errors.Is(err, coreclient.ErrAlreadyExists):
		existing, getErr := s.core.GetAccount(ctx, accountCode)
		if getErr != nil {
			return "", fmt.Errorf("recuperar cuenta existente: %w", getErr)
		}
		return existing.Id, nil
	default:
		return "", err
	}
}

// providerRef es la referencia estable que se envía al proveedor. Al ser derivada
// del expediente y el paso, un reintento reutiliza la verificación ya pagada en
// lugar de encargar una nueva.
func (s *Service) providerRef(app *Application, step Step) string {
	return fmt.Sprintf("app:%s:%s", app.ID, step)
}

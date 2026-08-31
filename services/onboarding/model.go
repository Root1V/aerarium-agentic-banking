package onboarding

import (
	"time"

	"github.com/aibank/aibank/adapters/kyc"
)

// Status es el estado del expediente de alta.
//
// La máquina de estados es explícita y persistida: un alta a medias tras una caída
// debe poder reanudarse desde donde quedó, no reiniciarse. Reiniciar significaría
// volver a pedirle documentos al cliente y volver a pagar verificaciones ya hechas.
type Status string

const (
	// StatusInProgress: hay pasos pendientes.
	StatusInProgress Status = "in_progress"
	// StatusManualReview: requiere decisión de un analista de cumplimiento.
	StatusManualReview Status = "manual_review"
	// StatusApproved: identidad verificada, screening resuelto y cuenta abierta.
	StatusApproved Status = "approved"
	// StatusRejected: no se abre cuenta.
	StatusRejected Status = "rejected"
)

// Step identifica cada etapa del alta.
type Step string

const (
	StepIdentityVerification Step = "identity_verification"
	StepSanctionsScreening   Step = "sanctions_screening"
	StepDecision             Step = "decision"
	StepAccountOpening       Step = "account_opening"
	// StepManualReview registra la decisión de un analista. NO forma parte del
	// pipeline: es un evento aparte, de otro actor, y debe convivir con la decisión
	// automática en el rastro. Si se sobrescribiera el paso `decision`, se perdería
	// justo la evidencia que un supervisor querría ver: quién aprobó a esta persona
	// pese a la coincidencia en listas.
	StepManualReview Step = "manual_review_decision"
)

// pipeline es el orden de ejecución. Cada paso se ejecuta una sola vez.
var pipeline = []Step{
	StepIdentityVerification,
	StepSanctionsScreening,
	StepDecision,
	StepAccountOpening,
}

// Application es el expediente de alta.
type Application struct {
	ID          string
	ExternalRef string
	CustomerID  string
	ProductCode string
	Status      Status
	Applicant   kyc.Applicant
	// AccountID se completa al aprobarse.
	AccountID      string
	DecisionReason string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StepRecord es el resultado registrado de un paso: la evidencia que sustenta la
// decisión ante el supervisor.
type StepRecord struct {
	Step        Step
	Outcome     string
	Provider    string
	ProviderRef string
	Detail      string
	CompletedAt time.Time
}

// SubmitRequest es una solicitud de alta llegada desde un canal.
type SubmitRequest struct {
	// ExternalRef identifica la solicitud en el canal de origen. Reenviarla no crea
	// un expediente nuevo.
	ExternalRef string
	CustomerID  string
	ProductCode string
	// AccountCode es el código con el que se abrirá la cuenta en el core.
	AccountCode string
	Applicant   kyc.Applicant
}

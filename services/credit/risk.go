package credit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Assessor evalúa a un cliente. Es un puerto: el motor de riesgo es un servicio
// aparte (Python, risk/), y las pruebas lo sustituyen.
type Assessor interface {
	Assess(ctx context.Context, req AssessmentRequest) (*Assessment, error)
}

// RiskMovement es un movimiento tal como lo entiende el motor: dirección desde
// el punto de vista del cliente.
type RiskMovement struct {
	OccurredAt   time.Time `json:"occurred_at"`
	Direction    string    `json:"direction"` // "in" u "out"
	AmountMicros int64     `json:"amount_micros"`
	Counterparty string    `json:"counterparty,omitempty"`
	Kind         string    `json:"kind,omitempty"`
}

type AssessmentRequest struct {
	CustomerID   string         `json:"customer_id"`
	AsOf         string         `json:"as_of"`
	EvaluatedAt  time.Time      `json:"evaluated_at"`
	Currency     string         `json:"currency"`
	ObservedDays int            `json:"observed_days"`
	Movements    []RiskMovement `json:"movements"`
}

// Assessment es el expediente que devuelve el motor. Record es el JSON completo,
// que se archiva tal cual.
type Assessment struct {
	Outcome      string   `json:"outcome"`
	LimitMicros  int64    `json:"limit_micros"`
	Currency     string   `json:"currency"`
	Score        int      `json:"score"`
	ModelVersion string   `json:"model_version"`
	Reasons      []string `json:"reasons"`
	Record       json.RawMessage
}

// HTTPAssessor consulta el servicio HTTP del motor de riesgo.
type HTTPAssessor struct {
	BaseURL string
	Client  *http.Client
}

func NewHTTPAssessor(baseURL string) *HTTPAssessor {
	return &HTTPAssessor{BaseURL: baseURL, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (a *HTTPAssessor) Assess(ctx context.Context, req AssessmentRequest) (*Assessment, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/assessments", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRiskUnavailable, err)
	}
	defer resp.Body.Close()

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %v", ErrRiskUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d: %s", ErrRiskUnavailable, resp.StatusCode, raw)
	}
	var out Assessment
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: expediente ilegible: %v", ErrRiskUnavailable, err)
	}
	out.Record = raw
	return &out, nil
}

// IncomeReader lee del motor el próximo ingreso esperado. Lo usa la previsión de
// saldo, con el mismo criterio de ingreso recurrente que la evaluación.
type IncomeReader interface {
	NextIncome(ctx context.Context, asOf time.Time, movements []RiskMovement) (*Income, error)
}

// Income es el ingreso recurrente que el motor espera, con su fecha.
type Income struct {
	Counterparty        string `json:"counterparty"`
	TypicalAmountMicros int64  `json:"typical_amount_micros"`
	Occurrences         int    `json:"occurrences"`
	// Cadence: "monthly", "biweekly" o "weekly".
	Cadence        string `json:"cadence"`
	LastReceivedOn string `json:"last_received_on"`
	NextExpectedOn string `json:"next_expected_on"`
}

// NextIncome pregunta al motor cuándo y cuánto se espera el próximo ingreso. nil
// si no hay uno que se pueda fechar.
func (a *HTTPAssessor) NextIncome(ctx context.Context, asOf time.Time, movements []RiskMovement) (*Income, error) {
	body, err := json.Marshal(map[string]any{"as_of": asOf.Format(time.DateOnly), "movements": movements})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/v1/income", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRiskUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrRiskUnavailable, resp.StatusCode)
	}
	var out struct {
		RecurringIncome *Income `json:"recurring_income"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: respuesta ilegible: %v", ErrRiskUnavailable, err)
	}
	return out.RecurringIncome, nil
}

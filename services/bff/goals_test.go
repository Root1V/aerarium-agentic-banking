package bff_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/aibank/aibank/services/forecast"
)

// Las metas por HTTP: crear una, darle una regla con la cara, y lo que la regla
// hace o no hace después.

// goalHolder es alguien con su teléfono vinculado y 250 de saldo de prueba.
func (e *passkeyEnv) goalHolder(t *testing.T) (h *handset, token, customer, account string) {
	t.Helper()
	reference, customer, account := e.approve(t)
	h = newHandset()
	status, linked := e.link(t, h, reference, "", "482916")
	if status != http.StatusCreated {
		t.Fatalf("vincular: %d %v", status, linked)
	}
	token = linked["token"].(string)
	if status, out := e.call(t, http.MethodPost, "/v1/sandbox/funds", token, map[string]any{"account_id": account}); status != http.StatusOK {
		t.Fatalf("saldo de prueba: %d %v", status, out)
	}
	return h, token, customer, account
}

func (e *passkeyEnv) createGoal(t *testing.T, token, account, name string, target int64, date string) map[string]any {
	t.Helper()
	body := map[string]any{"name": name, "target_micros": target}
	if date != "" {
		body["target_date"] = date
	}
	status, out := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/goals", token, body)
	if status != http.StatusCreated {
		t.Fatalf("crear meta: %d %v", status, out)
	}
	return out
}

// setRule pone la regla y la confirma con la cara.
func (e *passkeyEnv) setRule(t *testing.T, h *handset, token, goal string, amount int64, cadence string) map[string]any {
	t.Helper()
	body := map[string]any{"amount_micros": amount, "cadence": cadence}
	status, need := e.call(t, http.MethodPut, "/v1/goals/"+goal+"/rule", token, body)
	if status != http.StatusPreconditionRequired || need["code"] != "confirmation_required" {
		t.Fatalf("la regla no pidió la cara: %d %v", status, need)
	}
	body["confirmation"] = map[string]any{"challenge_id": need["challenge_id"], "method": "biometric",
		"signature": signB64(t, h.bio, need["payload"])}
	status, out := e.call(t, http.MethodPut, "/v1/goals/"+goal+"/rule", token, body)
	if status != http.StatusOK {
		t.Fatalf("regla confirmada: %d %v", status, out)
	}
	return out
}

func micros(v any) float64 { return v.(map[string]any)["amount_micros"].(float64) }

// runAgain corre la regla de la persona como si hubiera pasado `days`.
func (e *passkeyEnv) runAgain(t *testing.T, customer string, days int) {
	t.Helper()
	later := time.Now().AddDate(0, 0, days)
	e.goals.SetClock(func() time.Time { return later })
	t.Cleanup(func() { e.goals.SetClock(time.Now) })
	rules, err := e.goals.Rules(e.f.ctx, customer)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if _, err := e.goals.RunRule(e.f.ctx, r.ID); err != nil {
			t.Fatalf("correr la regla: %v", err)
		}
	}
}

func TestUnaReglaDeAhorroSeConfirmaConLaCaraYAportaHoy(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, _, account := e.goalHolder(t)
	goal := e.createGoal(t, token, account, "Viaje a Cusco", 100_000_000, "")
	id := goal["id"].(string)
	if micros(goal["saved"]) != 0 || goal["rule"] != nil {
		t.Fatalf("meta nueva: %v", goal)
	}

	// Lo que se le muestra al pedirle la cara: qué meta, cuánto y cada cuánto.
	status, need := e.call(t, http.MethodPut, "/v1/goals/"+id+"/rule", token,
		map[string]any{"amount_micros": 30_000_000, "cadence": "weekly"})
	summary, _ := need["summary"].(map[string]any)
	if status != http.StatusPreconditionRequired || summary["kind"] != "savings_rule" ||
		summary["to_name"] != "Viaje a Cusco" || summary["cadence"] != "weekly" || summary["amount_micros"] != float64(30_000_000) {
		t.Fatalf("pedir la cara: %d %v", status, need)
	}
	// La firma de una regla no sirve para otra más grande.
	status, out := e.call(t, http.MethodPut, "/v1/goals/"+id+"/rule", token, map[string]any{
		"amount_micros": 90_000_000, "cadence": "weekly",
		"confirmation": map[string]any{"challenge_id": need["challenge_id"], "method": "biometric",
			"signature": signB64(t, h.bio, need["payload"])}})
	if status != http.StatusForbidden {
		t.Fatalf("firma de otra regla: %d %v", status, out)
	}

	goal = e.setRule(t, h, token, id, 30_000_000, "weekly")
	if micros(goal["saved"]) != 30_000_000 || goal["rule"] == nil {
		t.Fatalf("el primer aporte es hoy: %v", goal)
	}
	if got := e.balance(t, token, account); got != 220_000_000 {
		t.Errorf("disponible tras el aporte: %v", got)
	}
	pace := goal["pace"].(map[string]any)
	if pace["reach_on"] != time.Now().UTC().AddDate(0, 0, 21).Format(time.DateOnly) {
		t.Errorf("con 30 por semana, faltan tres aportes: %v", pace)
	}

	_, detail := e.call(t, http.MethodGet, "/v1/goals/"+id, token, nil)
	history := detail["contributions"].([]any)
	first := history[0].(map[string]any)
	if len(history) != 1 || first["outcome"] != "saved" || micros(first["amount"]) != 30_000_000 {
		t.Fatalf("historial: %v", history)
	}

	// En el movimiento se ve quién apartó: la regla de esa meta.
	_, movement := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/movements/"+first["transaction_id"].(string), token, nil)
	auth, _ := movement["authorization"].(map[string]any)
	if auth["actor_kind"] != "rule" || auth["goal"] != "Viaje a Cusco" {
		t.Errorf("cómo se autorizó el aporte: %v", movement)
	}

	// Y en Permisos y privacidad, como un permiso más, que se quita con un toque.
	access := e.access(t, token)
	if rules := access["savings_rules"].([]any); len(rules) != 1 || rules[0].(map[string]any)["goal_name"] != "Viaje a Cusco" {
		t.Fatalf("reglas en el centro de permisos: %v", access["savings_rules"])
	}
	var programmed, saved bool
	for _, a := range access["activity"].([]any) {
		entry := a.(map[string]any)
		programmed = programmed || (entry["subject"] == "savings_rule" && entry["outcome"] == "confirmed")
		saved = saved || (entry["action"] == "auto_save" && entry["actor_name"] == "Regla de ahorro de «Viaje a Cusco»")
	}
	if !programmed || !saved {
		t.Errorf("la actividad no dice que se programó la regla ni que apartó: %v", access["activity"])
	}
	if status, _ := e.call(t, http.MethodDelete, "/v1/goals/"+id+"/rule", token, nil); status != http.StatusNoContent {
		t.Fatalf("quitar la regla: %d", status)
	}
	if rules := e.access(t, token)["savings_rules"].([]any); len(rules) != 0 {
		t.Errorf("la regla quitada sigue: %v", rules)
	}
	_, goal = e.call(t, http.MethodGet, "/v1/goals/"+id, token, nil)
	if goal["rule"] != nil || micros(goal["saved"]) != 30_000_000 {
		t.Errorf("quitar la regla deja la meta y lo juntado: %v", goal)
	}
}

// Antes de apartar, la regla mira la previsión: no aparta lo que dejaría a la
// persona sin colchón antes de cobrar, y lo dice.
func TestLaReglaNoApartaLoQueTeDejariaCorto(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, customer, account := e.goalHolder(t)
	income := time.Now().UTC().AddDate(0, 0, 20)
	// Lo más bajo antes del ingreso: 40. Con 5 al día, el colchón es 25: se
	// pueden apartar 15, no 30.
	e.goalsForecast.fixed = &forecast.Forecast{Lowest: forecast.Point{AmountMicros: 40_000_000},
		DailySpendMicros: 5_000_000, Income: &forecast.Income{ExpectedOn: income}}
	t.Cleanup(func() { e.goalsForecast.fixed = nil })

	id := e.createGoal(t, token, account, "Fondo", 200_000_000, "")["id"].(string)
	goal := e.setRule(t, h, token, id, 30_000_000, "weekly")
	if micros(goal["saved"]) != 15_000_000 {
		t.Fatalf("aporte reducido: %v", goal)
	}

	// La semana siguiente, lo más bajo es 20: no alcanza ni para el colchón.
	e.goalsForecast.fixed = &forecast.Forecast{Lowest: forecast.Point{AmountMicros: 20_000_000},
		DailySpendMicros: 5_000_000, Income: &forecast.Income{ExpectedOn: income}}
	e.runAgain(t, customer, 7)

	_, detail := e.call(t, http.MethodGet, "/v1/goals/"+id, token, nil)
	history := detail["contributions"].([]any)
	if len(history) != 2 {
		t.Fatalf("historial: %v", history)
	}
	skipped, reduced := history[0].(map[string]any), history[1].(map[string]any)
	if skipped["outcome"] != "skipped" || skipped["reason"] != "keeps_cushion" || micros(skipped["amount"]) != 0 ||
		micros(skipped["lowest"]) != 20_000_000 || skipped["income_on"] != income.Format(time.DateOnly) {
		t.Errorf("semana sin aporte: %v", skipped)
	}
	if reduced["outcome"] != "reduced" || reduced["reason"] != "keeps_cushion" || micros(reduced["asked"]) != 30_000_000 {
		t.Errorf("semana reducida: %v", reduced)
	}
	if micros(detail["saved"]) != 15_000_000 {
		t.Errorf("lo juntado: %v", detail["saved"])
	}
}

// Al cumplirse la meta, la regla se detiene sola: no sigue apartando.
func TestLaReglaSeDetieneAlCumplirLaMeta(t *testing.T) {
	e := newPasskeyEnv(t)
	h, token, customer, account := e.goalHolder(t)
	id := e.createGoal(t, token, account, "Bicicleta", 50_000_000, "")["id"].(string)
	e.setRule(t, h, token, id, 30_000_000, "weekly")

	// Faltaban 20: aparta 20, no 30, y la meta se cumple.
	e.runAgain(t, customer, 7)
	_, goal := e.call(t, http.MethodGet, "/v1/goals/"+id, token, nil)
	if micros(goal["saved"]) != 50_000_000 || goal["reached"] != true || goal["rule"] != nil {
		t.Fatalf("meta cumplida: %v", goal)
	}
	if rules := e.access(t, token)["savings_rules"].([]any); len(rules) != 0 {
		t.Errorf("la regla de una meta cumplida sigue en permisos: %v", rules)
	}
	e.runAgain(t, customer, 14)
	if got := e.balance(t, token, account); got != 200_000_000 {
		t.Errorf("siguió apartando después de cumplirla: disponible %v", got)
	}
}

func TestLoQueNoSePuedeConUnaMeta(t *testing.T) {
	e := newPasskeyEnv(t)
	_, token, _, account := e.goalHolder(t)
	id := e.createGoal(t, token, account, "Laptop", 40_000_000, time.Now().UTC().AddDate(0, 2, 0).Format(time.DateOnly))["id"].(string)

	if status, out := e.call(t, http.MethodPut, "/v1/goals/"+id+"/rule", token,
		map[string]any{"amount_micros": 50_000_000, "cadence": "monthly"}); status != http.StatusUnprocessableEntity || out["code"] != "rule_too_big" {
		t.Errorf("una regla mayor que la meta: %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/goals", token,
		map[string]any{"name": "Ayer", "target_micros": 1_000_000, "target_date": "2020-01-01"}); status != http.StatusBadRequest {
		t.Errorf("una meta para el pasado: %d %v", status, out)
	}
	if status, out := e.call(t, http.MethodPost, "/v1/accounts/"+account+"/pockets", token,
		map[string]any{"name": "Trampa", "purpose": "goal"}); status != http.StatusBadRequest {
		t.Errorf("un bolsillo de meta sin meta: %d %v", status, out)
	}

	// Cerrar la meta devuelve lo juntado.
	goalPocket := e.createGoal(t, token, account, "Para cerrar", 10_000_000, "")
	if status, out := e.pocketMove(t, token, goalPocket["pocket_id"].(string), "deposit", 5_000_000); status != http.StatusCreated {
		t.Fatalf("apartar a mano en la meta: %d %v", status, out)
	}
	if status, _ := e.call(t, http.MethodDelete, "/v1/goals/"+goalPocket["id"].(string), token, nil); status != http.StatusNoContent {
		t.Fatalf("cerrar la meta: %d", status)
	}
	if got := e.balance(t, token, account); got != 250_000_000 {
		t.Errorf("cerrar no devolvió lo juntado: %v", got)
	}
	_, list := e.call(t, http.MethodGet, "/v1/accounts/"+account+"/goals", token, nil)
	if goals := list["goals"].([]any); len(goals) != 1 || goals[0].(map[string]any)["pace"].(map[string]any)["suggested"] == nil {
		t.Errorf("queda la meta con fecha, con lo que habría que apartar: %v", list)
	}
}

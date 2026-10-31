package agents_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/aibank/aibank/services/agents"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func setup(t *testing.T) (*agents.Store, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://aibank:aibank_dev@localhost:5434/aibank?sslmode=disable"
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store := agents.NewStore(db)
	if err := store.Migrate(ctx); err != nil {
		t.Skipf("base no disponible: %v", err)
	}
	return store, ctx
}

func newKey(t *testing.T) (agents.JWK, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return agents.ToJWK(pub), priv
}

func TestUnAgenteSeRegistraConSuClaveYSoloLoVeSuPlataforma(t *testing.T) {
	store, ctx := setup(t)
	client := "plataforma-" + uuid.NewString()
	jwk, _ := newKey(t)

	agent, err := store.Register(ctx, client, "Agente de viajes", jwk)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "Agente de viajes" || len(agent.Keys) != 1 || agent.Keys[0].ExpiresAt != nil {
		t.Errorf("agente: %+v", agent)
	}
	if _, err := store.Get(ctx, "otra-"+uuid.NewString(), agent.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("otra plataforma lo ve: %v", err)
	}
	if _, err := store.Register(ctx, client, "Otro", jwk); !errors.Is(err, agents.ErrKeyInUse) {
		t.Errorf("la misma clave en dos agentes: %v", err)
	}
	if _, err := store.Register(ctx, client, " ", jwk); !errors.Is(err, agents.ErrBadName) {
		t.Errorf("sin nombre: %v", err)
	}
	if _, err := store.Register(ctx, client, "X", agents.JWK{Kty: "EC", Crv: "P-256", X: "abc"}); !errors.Is(err, agents.ErrInvalidKey) {
		t.Errorf("clave que no es Ed25519: %v", err)
	}
}

// Rotar no corta nada: la clave vieja sigue valiendo siete días, o hasta que se
// la retire.
func TestRotarUnaClaveDejaVivasLasDosDuranteSieteDias(t *testing.T) {
	store, ctx := setup(t)
	client := "plataforma-" + uuid.NewString()
	first, _ := newKey(t)
	agent, err := store.Register(ctx, client, "Agente", first)
	if err != nil {
		t.Fatal(err)
	}
	old := agent.Keys[0].ID

	second, _ := newKey(t)
	added, err := store.AddKey(ctx, client, agent.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, added.ID} {
		if _, err := store.KeyForSignature(ctx, id); err != nil {
			t.Errorf("clave %s durante la rotación: %v", id, err)
		}
	}

	later := time.Now().Add(agents.RotationWindow + time.Minute)
	store.SetClock(func() time.Time { return later })
	if _, err := store.KeyForSignature(ctx, old); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("la vieja pasados siete días: %v", err)
	}
	if _, err := store.KeyForSignature(ctx, added.ID); err != nil {
		t.Errorf("la nueva pasados siete días: %v", err)
	}
}

func TestLaUnicaClaveNoSeRetiraYUnaRetiradaNoFirma(t *testing.T) {
	store, ctx := setup(t)
	client := "plataforma-" + uuid.NewString()
	first, _ := newKey(t)
	agent, _ := store.Register(ctx, client, "Agente", first)
	old := agent.Keys[0].ID

	if err := store.RetireKey(ctx, client, agent.ID, old); !errors.Is(err, agents.ErrLastKey) {
		t.Fatalf("retirar la única: %v", err)
	}
	second, _ := newKey(t)
	if _, err := store.AddKey(ctx, client, agent.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireKey(ctx, client, agent.ID, old); err != nil {
		t.Fatalf("retirar la vieja: %v", err)
	}
	if _, err := store.KeyForSignature(ctx, old); !errors.Is(err, agents.ErrNotFound) {
		t.Errorf("una retirada sigue firmando: %v", err)
	}
}

func TestDarDeBajaAlAgenteApagaSusClaves(t *testing.T) {
	store, ctx := setup(t)
	client := "plataforma-" + uuid.NewString()
	jwk, _ := newKey(t)
	agent, _ := store.Register(ctx, client, "Agente", jwk)

	if err := store.Revoke(ctx, client, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.KeyForSignature(ctx, agent.Keys[0].ID); !errors.Is(err, agents.ErrRevoked) {
		t.Errorf("clave de un agente dado de baja: %v", err)
	}
	other, _ := newKey(t)
	if _, err := store.AddKey(ctx, client, agent.ID, other); !errors.Is(err, agents.ErrRevoked) {
		t.Errorf("añadir clave a un agente dado de baja: %v", err)
	}
}

// ---------------------------------------------------------------- la firma

func TestLaFirmaCubreMetodoRutaYCuerpo(t *testing.T) {
	store, ctx := setup(t)
	client := "plataforma-" + uuid.NewString()
	jwk, priv := newKey(t)
	agent, _ := store.Register(ctx, client, "Agente", jwk)
	keyID := agent.Keys[0].ID
	body := []byte(`{"amount":1000,"agent_id":"x"}`)
	now := time.Now()

	cases := []struct {
		name    string
		mutate  func() (method, path string, sent []byte, created time.Time, key ed25519.PrivateKey)
		wantErr error
	}{
		{"válida", func() (string, string, []byte, time.Time, ed25519.PrivateKey) {
			return "POST", "/v1/authorizations", body, now, priv
		}, nil},
		{"otro cuerpo", func() (string, string, []byte, time.Time, ed25519.PrivateKey) {
			return "POST", "/v1/authorizations", []byte(`{"amount":9000,"agent_id":"x"}`), now, priv
		}, agents.ErrSignatureInvalid},
		{"otra ruta", func() (string, string, []byte, time.Time, ed25519.PrivateKey) {
			return "POST", "/v1/accounts", body, now, priv
		}, agents.ErrSignatureInvalid},
		{"vieja", func() (string, string, []byte, time.Time, ed25519.PrivateKey) {
			return "POST", "/v1/authorizations", body, now.Add(-agents.MaxSignatureAge - time.Second), priv
		}, agents.ErrSignatureInvalid},
		{"otra clave", func() (string, string, []byte, time.Time, ed25519.PrivateKey) {
			_, other := newKey(t)
			return "POST", "/v1/authorizations", body, now, other
		}, agents.ErrSignatureInvalid},
	}
	for _, c := range cases {
		method, path, sent, created, key := c.mutate()
		// Se firma lo de siempre y se manda lo mutado: método, ruta o cuerpo.
		r := httptest.NewRequest("POST", "/v1/authorizations", bytes.NewReader(body))
		agents.Sign(r, body, keyID, key, created)
		r.Method = method
		r.URL.Path = path
		_, err := agents.Verify(ctx, r, sent, store.KeyForSignature, now)
		if !errors.Is(err, c.wantErr) && !(c.wantErr == nil && err == nil) {
			t.Errorf("%s: %v, quería %v", c.name, err, c.wantErr)
		}
	}

	r := httptest.NewRequest("POST", "/v1/authorizations", bytes.NewReader(body))
	if _, err := agents.Verify(ctx, r, body, store.KeyForSignature, now); !errors.Is(err, agents.ErrSignatureMissing) {
		t.Errorf("sin firma: %v", err)
	}
	r = httptest.NewRequest("POST", "/v1/authorizations", bytes.NewReader(body))
	agents.Sign(r, body, keyID, priv, now)
	signer, err := agents.Verify(ctx, r, body, store.KeyForSignature, now)
	if err != nil || signer.AgentID != agent.ID || signer.ClientID != client {
		t.Errorf("quién firmó: %+v, %v", signer, err)
	}
}

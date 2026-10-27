package bff_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"testing"
)

// El día a día por HTTP: alta, vincular el teléfono, salir, y volver a entrar
// con la cara o con la clave.

type handset struct {
	bio, possession *ecdsa.PrivateKey
	id              string
}

func newHandset() *handset {
	bio, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	possession, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &handset{bio: bio, possession: possession}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func pub(k *ecdsa.PrivateKey) string {
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return b64(der)
}

func signB64(t *testing.T, k *ecdsa.PrivateKey, payload any) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(payload.(string))
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	digest := sha256.Sum256(raw)
	sig, _ := ecdsa.SignASN1(rand.Reader, k, digest[:])
	return b64(sig)
}

// link vincula el teléfono con la referencia del alta (o con sesión si token).
func (e *passkeyEnv) link(t *testing.T, h *handset, reference, token, pin string) (int, map[string]any) {
	t.Helper()
	_, c := e.call(t, http.MethodPost, "/v1/devices/registration/options", "", map[string]any{})
	status, out := e.call(t, http.MethodPost, "/v1/devices/registration", token, map[string]any{
		"challenge_id":         c["challenge_id"],
		"reference":            reference,
		"biometric_key":        pub(h.bio),
		"possession_key":       pub(h.possession),
		"biometric_signature":  signB64(t, h.bio, c["payload"]),
		"possession_signature": signB64(t, h.possession, c["payload"]),
		"pin":                  pin,
		"name":                 "iPhone de prueba",
		"hardware_backed":      true,
	})
	if status == http.StatusCreated {
		h.id = out["device_id"].(string)
	}
	return status, out
}

func (e *passkeyEnv) unlock(t *testing.T, h *handset, method, pin string) (int, map[string]any) {
	t.Helper()
	status, c := e.call(t, http.MethodPost, "/v1/devices/login/options", "", map[string]any{"device_id": h.id})
	if status != http.StatusOK {
		// Un teléfono bloqueado o desvinculado no recibe ni el desafío.
		return status, c
	}
	key := h.bio
	if method == "pin" {
		key = h.possession
	}
	return e.call(t, http.MethodPost, "/v1/devices/login", "", map[string]any{
		"challenge_id": c["challenge_id"], "method": method,
		"signature": signB64(t, key, c["payload"]), "pin": pin,
	})
}

func TestDelAltaAlTelefonoQueEntraConLaCara(t *testing.T) {
	e := newPasskeyEnv(t)
	reference, customer, account := e.approve(t)
	h := newHandset()

	status, linked := e.link(t, h, reference, "", "482916")
	if status != http.StatusCreated || linked["customer_id"] != customer {
		t.Fatalf("vincular: %d %v", status, linked)
	}
	if status, _ := e.call(t, http.MethodPost, "/v1/session/logout", linked["token"].(string), nil); status != http.StatusNoContent {
		t.Fatalf("salir: %d", status)
	}

	status, session := e.unlock(t, h, "biometric", "")
	if status != http.StatusOK || session["customer_id"] != customer {
		t.Fatalf("entrar con la cara: %d %v", status, session)
	}
	if status, _ := e.call(t, http.MethodGet, "/v1/home?account_id="+account, session["token"].(string), nil); status != http.StatusOK {
		t.Errorf("inicio: %d", status)
	}

	status, session = e.unlock(t, h, "pin", "482916")
	if status != http.StatusOK {
		t.Fatalf("entrar con la clave: %d %v", status, session)
	}
}

func TestLaClaveMalaDiceCuantosIntentosQuedanYLuegoBloquea(t *testing.T) {
	e := newPasskeyEnv(t)
	reference, _, _ := e.approve(t)
	h := newHandset()
	e.link(t, h, reference, "", "482916")

	status, out := e.unlock(t, h, "pin", "135790")
	if status != http.StatusUnauthorized || out["code"] != "pin_incorrect" || out["remaining_attempts"] != float64(4) {
		t.Fatalf("primer intento: %d %v", status, out)
	}
	for i := 0; i < 4; i++ {
		status, out = e.unlock(t, h, "pin", "135790")
	}
	if status != http.StatusForbidden || out["code"] != "device_locked" {
		t.Fatalf("quinto intento: %d %v", status, out)
	}
	if status, out := e.unlock(t, h, "biometric", ""); status != http.StatusForbidden || out["code"] != "device_locked" {
		t.Errorf("la cara tras el bloqueo: %d %v", status, out)
	}
}

func TestUnaClaveDebilSeRechazaAntesDeVincular(t *testing.T) {
	e := newPasskeyEnv(t)
	reference, _, _ := e.approve(t)
	if status, out := e.link(t, newHandset(), reference, "", "123456"); status != http.StatusBadRequest || out["code"] != "weak_pin" {
		t.Errorf("123456: %d %v", status, out)
	}
	if status, _ := e.link(t, newHandset(), reference, "", "482916"); status != http.StatusCreated {
		t.Errorf("la referencia se gastó con la clave débil: %d", status)
	}
}

// Tras entrar con la passkey en un teléfono nuevo, se vincula con la sesión.
func TestVincularOtroTelefonoConLaSesionDeLaPasskey(t *testing.T) {
	e := newPasskeyEnv(t)
	token, customer, _, _ := e.onboard(t)
	h := newHandset()
	if status, out := e.link(t, h, "", token, "907251"); status != http.StatusCreated || out["customer_id"] != customer {
		t.Fatalf("vincular con sesión: %d %v", status, out)
	}
	if status, _ := e.unlock(t, h, "biometric", ""); status != http.StatusOK {
		t.Errorf("entrar: %d", status)
	}
}

func TestDesvincularElTelefono(t *testing.T) {
	e := newPasskeyEnv(t)
	reference, _, _ := e.approve(t)
	h := newHandset()
	_, linked := e.link(t, h, reference, "", "482916")
	token := linked["token"].(string)

	other := newHandset()
	otherRef, _, _ := e.approve(t)
	_, otherLinked := e.link(t, other, otherRef, "", "482916")
	if status, _ := e.call(t, http.MethodDelete, "/v1/devices/"+other.id, token, nil); status != http.StatusNotFound {
		t.Errorf("desvincular el teléfono de otro: %d", status)
	}
	_ = otherLinked

	if status, _ := e.call(t, http.MethodDelete, "/v1/devices/"+h.id, token, nil); status != http.StatusNoContent {
		t.Fatalf("desvincular: %d", status)
	}
	if status, _ := e.call(t, http.MethodGet, "/v1/accounts", token, nil); status != http.StatusUnauthorized {
		t.Errorf("la sesión del teléfono desvinculado sigue: %d", status)
	}
	if status, out := e.unlock(t, h, "biometric", ""); status != http.StatusUnauthorized || out["code"] != "device_rejected" {
		t.Errorf("entrar desvinculado: %d %v", status, out)
	}
}

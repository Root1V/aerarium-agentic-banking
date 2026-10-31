package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// La firma del agente sigue RFC 9421 (HTTP Message Signatures), el mecanismo
// del Trusted Agent Protocol de Visa y de Web Bot Auth. El perfil que se exige
// es uno solo, a propósito:
//
//	Content-Digest: sha-256=:<base64 del SHA-256 del cuerpo>:
//	Signature-Input: sig1=("@method" "@path" "content-digest");created=<unix>;keyid="key_…";alg="ed25519"
//	Signature: sig1=:<base64 de la firma Ed25519>:
//
// Cubre el método, la ruta y el cuerpo entero (a través de su digest): monto,
// receptor, agente e intención quedan firmados. `created` acota la reutilización
// de una firma a MaxSignatureAge; dentro de esa ventana, repetir la petición es
// un reintento, y la clave de idempotencia lo vuelve inofensivo.

// MaxSignatureAge es cuánto vale una firma desde que se creó.
const MaxSignatureAge = 5 * time.Minute

// requiredComponents es lo que la firma tiene que cubrir, en este orden.
var requiredComponents = []string{"@method", "@path", "content-digest"}

var (
	// ErrSignatureMissing: la petición no viene firmada.
	ErrSignatureMissing = errors.New("falta la firma del agente")
	// ErrSignatureInvalid: viene firmada, pero no verifica. Para afuera, todos
	// los motivos son el mismo; el motivo concreto va envuelto, para el log.
	ErrSignatureInvalid = errors.New("la firma del agente no es válida")
)

// KeyLookup resuelve el `keyid` de la firma.
type KeyLookup func(ctx context.Context, keyID string) (*Signer, error)

// Verify comprueba la firma de una petición con su cuerpo ya leído, y devuelve
// quién firmó.
func Verify(ctx context.Context, r *http.Request, body []byte, lookup KeyLookup, now time.Time) (*Signer, error) {
	input, signature := r.Header.Get("Signature-Input"), r.Header.Get("Signature")
	if input == "" && signature == "" {
		return nil, ErrSignatureMissing
	}
	invalid := func(why string) error { return fmt.Errorf("%w: %s", ErrSignatureInvalid, why) }

	label, params, err := parseSignatureInput(input)
	if err != nil {
		return nil, invalid(err.Error())
	}
	sig, err := parseSignature(signature, label)
	if err != nil {
		return nil, invalid(err.Error())
	}
	if !slices.Equal(params.components, requiredComponents) {
		return nil, invalid("la firma tiene que cubrir @method, @path y content-digest")
	}
	if params.alg != "" && params.alg != "ed25519" {
		return nil, invalid("algoritmo no admitido")
	}
	age := now.Sub(time.Unix(params.created, 0))
	if age > MaxSignatureAge || age < -time.Minute {
		return nil, invalid("firma vencida o con fecha futura")
	}
	if err := checkDigest(r.Header.Get("Content-Digest"), body); err != nil {
		return nil, invalid(err.Error())
	}

	signer, err := lookup(ctx, params.keyID)
	if err != nil {
		return nil, invalid("clave desconocida, vencida o de un agente dado de baja")
	}
	base := signatureBase(r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Digest"), params.raw)
	if !ed25519.Verify(signer.PublicKey, []byte(base), sig) {
		return nil, invalid("la firma no corresponde a la petición")
	}
	return signer, nil
}

// Sign firma una petición según el perfil de arriba. Es lo que hace la
// plataforma por cada pago; aquí sirve para las pruebas y la demo.
func Sign(r *http.Request, body []byte, keyID string, key ed25519.PrivateKey, created time.Time) {
	sum := sha256.Sum256(body)
	digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	r.Header.Set("Content-Digest", digest)
	params := fmt.Sprintf(`("@method" "@path" "content-digest");created=%d;keyid=%q;alg="ed25519"`,
		created.Unix(), keyID)
	base := signatureBase(r.Method, r.URL.EscapedPath(), digest, params)
	r.Header.Set("Signature-Input", "sig1="+params)
	r.Header.Set("Signature", "sig1=:"+base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(base)))+":")
}

// signatureBase arma lo que se firma (RFC 9421 §2.5).
func signatureBase(method, path, digest, params string) string {
	return `"@method": ` + strings.ToUpper(method) + "\n" +
		`"@path": ` + path + "\n" +
		`"content-digest": ` + digest + "\n" +
		`"@signature-params": ` + params
}

type sigParams struct {
	components []string
	created    int64
	keyID      string
	alg        string
	// raw es el valor tal como llegó, que es lo que entra en la base firmada.
	raw string
}

// parseSignatureInput lee `label=("a" "b");k=v;…`. Admite una sola firma.
func parseSignatureInput(h string) (string, sigParams, error) {
	var p sigParams
	label, rest, ok := strings.Cut(strings.TrimSpace(h), "=")
	if !ok || label == "" || strings.Contains(rest, ",") {
		return "", p, errors.New("Signature-Input ilegible (una sola firma)")
	}
	p.raw = rest
	if !strings.HasPrefix(rest, "(") {
		return "", p, errors.New("Signature-Input sin lista de componentes")
	}
	end := strings.Index(rest, ")")
	if end < 0 {
		return "", p, errors.New("Signature-Input sin cerrar")
	}
	for _, c := range strings.Fields(rest[1:end]) {
		unq, err := strconv.Unquote(c)
		if err != nil {
			return "", p, errors.New("componente sin comillas")
		}
		p.components = append(p.components, unq)
	}
	for _, kv := range strings.Split(rest[end+1:], ";") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "created":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return "", p, errors.New("created inválido")
			}
			p.created = n
		case "keyid", "alg":
			unq, err := strconv.Unquote(v)
			if err != nil {
				return "", p, errors.New(k + " sin comillas")
			}
			if k == "keyid" {
				p.keyID = unq
			} else {
				p.alg = unq
			}
		}
	}
	if p.created == 0 || p.keyID == "" {
		return "", p, errors.New("faltan created o keyid")
	}
	return label, p, nil
}

// parseSignature lee `label=:base64:`.
func parseSignature(h, label string) ([]byte, error) {
	got, value, ok := strings.Cut(strings.TrimSpace(h), "=")
	if !ok || got != label || !strings.HasPrefix(value, ":") || !strings.HasSuffix(value, ":") || len(value) < 2 {
		return nil, errors.New("Signature ilegible o de otra etiqueta")
	}
	sig, err := base64.StdEncoding.DecodeString(value[1 : len(value)-1])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, errors.New("Signature no es una firma Ed25519")
	}
	return sig, nil
}

// checkDigest comprueba que Content-Digest sea el SHA-256 del cuerpo recibido.
func checkDigest(h string, body []byte) error {
	value, ok := strings.CutPrefix(strings.TrimSpace(h), "sha-256=:")
	if !ok || !strings.HasSuffix(value, ":") {
		return errors.New("falta Content-Digest sha-256")
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(value, ":"))
	if err != nil {
		return errors.New("Content-Digest ilegible")
	}
	sum := sha256.Sum256(body)
	if subtle.ConstantTimeCompare(want, sum[:]) != 1 {
		return errors.New("Content-Digest no corresponde al cuerpo")
	}
	return nil
}

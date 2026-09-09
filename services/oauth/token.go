package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Vigencia de un token de acceso.
//
// Una hora es lo que el contrato con Mercatus fija y es un equilibrio razonable:
// suficiente para que el cliente cachee en vez de pedir uno por llamada, y corto
// para que un token filtrado deje de servir solo en vez de quedar activo hasta
// que alguien lo note.
const TokenTTL = time.Hour

// Claims son los datos que viajan dentro del token.
type Claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"` // client_id
	Scopes    []string `json:"scope_list"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	TokenID   string   `json:"jti"`
}

// HasScope indica si el token autoriza una operación.
func (c *Claims) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

var (
	ErrTokenMalformed = errors.New("token malformado")
	ErrTokenSignature = errors.New("firma inválida")
	ErrTokenExpired   = errors.New("token expirado")
)

// Issuer emite y verifica tokens de acceso.
//
// # Por qué el JWT está escrito a mano y no con una librería
//
// El formato es HS256, que son cincuenta líneas de HMAC y base64url. Lo que se
// gana escribiéndolo es lo que se pierde usando una librería genérica: aquí el
// algoritmo NUNCA se lee del token. La familia de vulnerabilidades más conocida
// de JWT —confusión de algoritmo, incluido `alg: none`— existe porque los
// verificadores genéricos preguntan al token cómo verificarse. Este no pregunta:
// verifica con HS256 y con la clave del banco, y un token que diga cualquier otra
// cosa en su cabecera se rechaza sin llegar a la firma.
//
// La segunda clave existe para rotar la clave de firma sin invalidar los tokens
// ya emitidos: se firma con la nueva y se sigue aceptando la anterior hasta que
// caduquen solos.
type Issuer struct {
	issuer      string
	signingKey  []byte
	previousKey []byte
	now         func() time.Time
}

// NewIssuer crea un emisor. `previousKey` puede ser nil.
func NewIssuer(issuer string, signingKey, previousKey []byte) (*Issuer, error) {
	// 32 bytes = el tamaño de salida de SHA-256. Una clave más corta no aporta
	// más seguridad que su propia longitud, por mucho que HMAC la acepte.
	if len(signingKey) < 32 {
		return nil, fmt.Errorf("la clave de firma necesita al menos 32 bytes, tiene %d", len(signingKey))
	}
	return &Issuer{
		issuer:      issuer,
		signingKey:  signingKey,
		previousKey: previousKey,
		now:         time.Now,
	}, nil
}

// header fijo. No se serializa desde una estructura para dejar claro que no hay
// ningún camino por el que el algoritmo pueda variar.
const fixedHeader = `{"alg":"HS256","typ":"JWT"}`

// Issue emite un token para un cliente con los scopes concedidos.
func (i *Issuer) Issue(clientID string, scopes []string, tokenID string) (string, *Claims, error) {
	now := i.now().UTC()
	claims := &Claims{
		Issuer:    i.issuer,
		Subject:   clientID,
		Scopes:    scopes,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(TokenTTL).Unix(),
		TokenID:   tokenID,
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", nil, fmt.Errorf("serializar claims: %w", err)
	}

	signingInput := encode([]byte(fixedHeader)) + "." + encode(payload)
	signature := sign(i.signingKey, signingInput)

	return signingInput + "." + signature, claims, nil
}

// Verify comprueba firma y vigencia, y devuelve las claims.
func (i *Issuer) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrTokenMalformed
	}

	header, err := decode(parts[0])
	if err != nil || string(header) != fixedHeader {
		// Cabecera distinta a la única que emitimos: no se sigue adelante. Aquí
		// es donde muere `alg: none` y la confusión de algoritmo.
		return nil, ErrTokenMalformed
	}

	signingInput := parts[0] + "." + parts[1]
	if !i.signatureMatches(signingInput, parts[2]) {
		return nil, ErrTokenSignature
	}

	payload, err := decode(parts[1])
	if err != nil {
		return nil, ErrTokenMalformed
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrTokenMalformed
	}

	if claims.Issuer != i.issuer {
		return nil, ErrTokenSignature
	}
	if i.now().UTC().Unix() >= claims.ExpiresAt {
		return nil, ErrTokenExpired
	}

	return &claims, nil
}

// signatureMatches acepta la clave vigente o la anterior.
func (i *Issuer) signatureMatches(signingInput, signature string) bool {
	if hmac.Equal([]byte(sign(i.signingKey, signingInput)), []byte(signature)) {
		return true
	}
	if len(i.previousKey) == 0 {
		return false
	}
	return hmac.Equal([]byte(sign(i.previousKey, signingInput)), []byte(signature))
}

func sign(key []byte, input string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(input))
	return encode(mac.Sum(nil))
}

func encode(b []byte) string  { return base64.RawURLEncoding.EncodeToString(b) }
func decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

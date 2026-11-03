package receipts_test

import (
	"testing"

	"github.com/aibank/aibank/services/receipts"
)

// El código sale de lo que la persona tenga a mano: suelto, el enlace o la
// constancia entera pegada de un chat.
func TestElCodigoSaleDeLoQueSePegue(t *testing.T) {
	whole := "Aerarium · Constancia de operación\nAna E. → Juan P.\nMonto: 50,00 USD\n" +
		"Fecha: 3 de noviembre de 2026, 10:15\nCódigo de verificación: K7Q2M-9XR4T\n" +
		"Comprueba que es real en: http://192.168.68.63:38080/c/K7Q2M-9XR4T"
	for name, input := range map[string]string{
		"suelto":        "K7Q2M-9XR4T",
		"minúsculas":    "k7q2m9xr4t",
		"con espacios":  "  K7Q2M 9XR4T ",
		"el enlace":     "http://192.168.68.63:38080/c/K7Q2M-9XR4T",
		"la constancia": whole,
	} {
		if got := receipts.Normalize(input); got != "K7Q2M-9XR4T" {
			t.Errorf("%s: %q", name, got)
		}
	}
	// O y 0, I y 1 se confunden al dictarlo.
	if got := receipts.Normalize("ABCDO-FGHJI"); got != "ABCD0-FGHJ1" {
		t.Errorf("letras que se confunden: %q", got)
	}
}

package ratelimit

import (
	"testing"
	"time"
)

func TestElCuboDeFichasSeRellenaConElTiempo(t *testing.T) {
	limiter := NewLimiter(10, 2)
	base := time.Now()
	limiter.now = func() time.Time { return base }

	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("la primera petición debería pasar")
	}
	limiter.Allow("k")
	if ok, wait := limiter.Allow("k"); ok || wait <= 0 {
		t.Fatalf("la tercera debería rebotar con espera, ok=%v wait=%v", ok, wait)
	}

	// A 10 por segundo, un décimo de segundo devuelve una ficha.
	limiter.now = func() time.Time { return base.Add(150 * time.Millisecond) }
	if ok, _ := limiter.Allow("k"); !ok {
		t.Error("el cubo no se rellenó con el paso del tiempo")
	}
}

func TestElLimitePorCuentaNoAfectaAOtraCuenta(t *testing.T) {
	limiter := NewLimiter(1, 1)
	limiter.Allow("cuenta-a")

	// Un agente en bucle no puede consumir la cuota de los demás.
	if ok, _ := limiter.Allow("cuenta-b"); !ok {
		t.Error("una cuenta agotó la cuota de otra")
	}
}

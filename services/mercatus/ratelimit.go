package mercatus

import (
	"sync"
	"time"
)

// Límites por defecto.
//
// 50 peticiones por segundo sostenidas es lo publicado en la respuesta a
// Mercatus. El límite por sub-cuenta es más bajo a propósito: la integración
// entera comparte una credencial, así que un agente en bucle podría consumir la
// cuota de todos los demás. Acotarlo por cuenta convierte ese incidente en un
// problema de un solo agente.
const (
	DefaultClientRate   = 50
	DefaultClientBurst  = 100
	DefaultAccountRate  = 10
	DefaultAccountBurst = 20
)

// bucket es un cubo de fichas: se rellena a `rate` por segundo hasta `burst`.
//
// Se prefiere a una ventana fija porque una ventana permite el doble del límite
// en el cambio de ventana —50 al final de un segundo y 50 al principio del
// siguiente— justo cuando un cliente reintenta en ráfaga.
type bucket struct {
	tokens   float64
	lastFill time.Time
}

// Limiter aplica límites por clave.
//
// Es un limitador POR INSTANCIA: con varias réplicas detrás de un balanceador, el
// límite efectivo se multiplica por el número de réplicas. Está bien para el
// sandbox y para la primera etapa de producción; cuando haya más de una réplica
// hay que moverlo a un almacén compartido, y esto queda anotado como deuda.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	now     func() time.Time
}

func NewLimiter(ratePerSecond, burst int) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    float64(ratePerSecond),
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow consume una ficha. Devuelve si se permite y, si no, cuánto esperar.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastFill: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
		b.lastFill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	// Cuánto falta para que se acumule una ficha. Va en Retry-After para que el
	// cliente espere lo justo en vez de adivinar un backoff.
	missing := 1 - b.tokens
	return false, time.Duration(missing/l.rate*float64(time.Second)) + time.Millisecond
}

// Forget descarta el estado de una clave. Sirve para las pruebas y para liberar
// memoria de integraciones dadas de baja.
func (l *Limiter) Forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

package mercatus

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

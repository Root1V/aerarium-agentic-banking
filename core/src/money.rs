//! La unidad monetaria del sistema.
//!
//! **Todo importe en AIBank es un `i64` en micras: millonésimas (10⁻⁶) de la
//! unidad mayor.** Un dólar son `1_000_000`; un centavo, `10_000`; un milésimo
//! de dólar —el precio real de una llamada a un servicio de agente—, `1_000`.
//!
//! # Por qué no centavos
//!
//! La escala 2 alcanza para una cuenta de personas y se queda corta en el
//! momento en que el banco liquida pagos máquina a máquina. Un servicio que
//! cobra $0,001 por llamada no se puede representar en centavos: redondear
//! hacia abajo cobra $0,00 y el pago desaparece; hacia arriba cobra diez veces
//! de más. No es un caso de borde, es el caso de uso.
//!
//! Convertir en el borde —guardar centavos y recibir micras en la API— tiene el
//! mismo defecto con un disfraz: el error de redondeo se acumula asiento por
//! asiento en el único lugar donde no puede acumularse. La escala del ledger es
//! la escala del negocio.
//!
//! # Qué NO cambia
//!
//! La escala es de almacenamiento, no de presentación. La app le sigue mostrando
//! dos decimales a una persona; quien necesita seis, los pide. Formatear es
//! responsabilidad del canal.
//!
//! # Rango
//!
//! `i64` en micras cubre ±9,2 billones de unidades (±9,2 × 10¹² USD). Para
//! comparar: es tres órdenes de magnitud más que la base monetaria de cualquier
//! economía de la región. El techo no es una restricción práctica.

/// Micras por unidad mayor (1 USD = 1_000_000 micras).
pub const MICROS_PER_UNIT: i64 = 1_000_000;

/// Micras por centavo. Es el factor de conversión desde la escala anterior y el
/// que usa cualquier riel externo que hable en centavos.
pub const MICROS_PER_CENT: i64 = 10_000;

/// Convierte unidades mayores enteras a micras.
///
/// Existe para que el código de negocio no escriba ceros a mano: `from_units(50)`
/// se lee mejor —y se equivoca menos— que `50_000_000`.
pub const fn from_units(units: i64) -> i64 {
    units * MICROS_PER_UNIT
}

/// Convierte centavos a micras. Es exacta: toda cantidad de centavos tiene
/// representación en micras.
pub const fn from_cents(cents: i64) -> i64 {
    cents * MICROS_PER_CENT
}

/// Convierte micras a centavos truncando hacia cero.
///
/// **Devuelve también el resto**, y ese resto es el punto: quien convierte a una
/// escala más gruesa está perdiendo información y tiene que decidir qué hace con
/// ella. Un adaptador hacia un riel que solo habla en centavos no puede tirar la
/// fracción en silencio — o la acumula, o la rechaza, pero no la ignora.
pub const fn to_cents(micros: i64) -> (i64, i64) {
    (micros / MICROS_PER_CENT, micros % MICROS_PER_CENT)
}

/// Indica si un importe cae exactamente en un centavo.
pub const fn is_whole_cents(micros: i64) -> bool {
    micros % MICROS_PER_CENT == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn un_dolar_son_un_millon_de_micras() {
        assert_eq!(from_units(1), 1_000_000);
        assert_eq!(from_cents(100), 1_000_000);
    }

    #[test]
    fn representa_el_precio_que_los_centavos_no_pueden() {
        // El caso que motiva toda la escala: $0,001 por llamada.
        let precio = 1_000;
        assert_eq!(to_cents(precio), (0, 1_000), "en centavos sería 0: se perdería el pago");
        assert!(!is_whole_cents(precio));
    }

    #[test]
    fn convertir_a_centavos_expone_el_resto_en_vez_de_esconderlo() {
        let (cents, remainder) = to_cents(1_234_567);
        assert_eq!(cents, 123);
        assert_eq!(remainder, 4_567, "el adaptador tiene que decidir qué hace con esto");
    }

    #[test]
    fn el_rango_cubre_cualquier_importe_bancario_real() {
        // Un billón de dólares —tres veces el PIB de Brasil— entra sin desbordar.
        let un_billon_usd: i64 = 1_000_000_000_000;
        assert_eq!(
            un_billon_usd.checked_mul(MICROS_PER_UNIT),
            Some(1_000_000_000_000_000_000),
        );
    }

    #[test]
    fn los_importes_negativos_truncan_hacia_cero_en_ambos_lados() {
        // Un saldo puede ser negativo (cuenta con sobregiro autorizado); la
        // conversión tiene que ser simétrica para no crear un centavo de la nada.
        assert_eq!(to_cents(-1_234_567), (-123, -4_567));
        assert_eq!(to_cents(-10_000), (-1, 0));
    }
}

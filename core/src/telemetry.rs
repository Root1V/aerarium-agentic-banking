//! Trazado distribuido.
//!
//! Un pago atraviesa varios procesos y lenguajes: la app llama al BFF, el BFF al
//! core, el core encola un evento y un relay lo publica minutos después. Sin un
//! hilo que una todo eso, investigar "qué pasó con esta transferencia" es leer
//! cuatro registros distintos y adivinar cuál corresponde a cuál.
//!
//! Dos piezas hacen falta y ninguna es automática:
//!
//! 1. **Propagación entre procesos**: el contexto W3C viaja en la metadata gRPC.
//! 2. **Propagación en el tiempo**: el evento del outbox se publica en OTRO
//!    proceso y más tarde. El contexto se guarda CON el evento para poder
//!    enlazar la publicación con la operación que la originó.
//!
//! Y una regla que no se negocia: en una traza NUNCA entran datos personales ni
//! secretos. Las trazas salen a herramientas de terceros; un número de documento
//! o un PAN ahí dentro es una fuga.

use opentelemetry::propagation::{Extractor, Injector, TextMapPropagator};
use opentelemetry::trace::TraceContextExt;
use opentelemetry::Context;
use opentelemetry_sdk::propagation::TraceContextPropagator;
use std::collections::HashMap;
use tonic::metadata::{MetadataKey, MetadataMap, MetadataValue};

/// Lee el contexto de traza desde la metadata gRPC entrante.
struct MetadataExtractor<'a>(&'a MetadataMap);

impl Extractor for MetadataExtractor<'_> {
    fn get(&self, key: &str) -> Option<&str> {
        self.0.get(key).and_then(|value| value.to_str().ok())
    }

    fn keys(&self) -> Vec<&str> {
        self.0
            .keys()
            .filter_map(|key| match key {
                tonic::metadata::KeyRef::Ascii(k) => Some(k.as_str()),
                tonic::metadata::KeyRef::Binary(_) => None,
            })
            .collect()
    }
}

/// Escribe el contexto de traza en un mapa de texto plano.
#[derive(Default)]
struct MapInjector(HashMap<String, String>);

impl Injector for MapInjector {
    fn set(&mut self, key: &str, value: String) {
        self.0.insert(key.to_owned(), value);
    }
}

struct MapExtractor<'a>(&'a HashMap<String, String>);

impl Extractor for MapExtractor<'_> {
    fn get(&self, key: &str) -> Option<&str> {
        self.0.get(key).map(String::as_str)
    }
    fn keys(&self) -> Vec<&str> {
        self.0.keys().map(String::as_str).collect()
    }
}

/// Recupera el contexto de traza de una petición gRPC entrante.
///
/// Si el llamador no envió contexto —una llamada suelta, una prueba— devuelve un
/// contexto vacío en vez de fallar: la telemetría nunca debe tumbar una operación.
pub fn context_from_metadata(metadata: &MetadataMap) -> Context {
    TraceContextPropagator::new().extract(&MetadataExtractor(metadata))
}

/// Inserta el contexto actual en la metadata de una llamada saliente.
pub fn inject_into_metadata(context: &Context, metadata: &mut MetadataMap) {
    let mut carrier = MapInjector::default();
    TraceContextPropagator::new().inject_context(context, &mut carrier);

    for (key, value) in carrier.0 {
        if let (Ok(key), Ok(value)) = (
            MetadataKey::from_bytes(key.as_bytes()),
            MetadataValue::try_from(value.as_str()),
        ) {
            metadata.insert(key, value);
        }
    }
}

/// Serializa el contexto actual para guardarlo junto a un evento del outbox.
///
/// Es lo que permite, minutos después y en otro proceso, enlazar la publicación
/// con la transacción que la originó.
pub fn serialize_context(context: &Context) -> Option<String> {
    if !context.span().span_context().is_valid() {
        return None;
    }
    let mut carrier = MapInjector::default();
    TraceContextPropagator::new().inject_context(context, &mut carrier);
    carrier.0.get("traceparent").cloned()
}

/// Reconstruye el contexto guardado con un evento.
pub fn deserialize_context(traceparent: &str) -> Context {
    let carrier = HashMap::from([("traceparent".to_string(), traceparent.to_string())]);
    TraceContextPropagator::new().extract(&MapExtractor(&carrier))
}

/// Identificador de traza legible, para correlacionar con los registros.
pub fn trace_id_of(context: &Context) -> Option<String> {
    let span_context = context.span().span_context().clone();
    span_context
        .is_valid()
        .then(|| span_context.trace_id().to_string())
}

// ---------------------------------------------------------------- guarda de PII

/// Atributos que JAMÁS pueden aparecer en una traza.
///
/// Las trazas salen a herramientas de terceros y se conservan mucho tiempo. Un
/// número de tarjeta o de documento ahí dentro es una fuga de datos, y además
/// arrastraría a todo el sistema al alcance de PCI.
const FORBIDDEN_ATTRIBUTES: &[&str] = &[
    "pan",
    "card_number",
    "cvv",
    "document_number",
    "full_name",
    "email",
    "phone",
    "password",
    "token",
    "authorization",
    "secret",
    "api_key",
];

/// Indica si un atributo puede registrarse en una traza.
///
/// Es deliberadamente estricto: compara por subcadena, así que `customer_email` o
/// `card_number_hash` también quedan fuera. Ante la duda, no se registra.
pub fn is_safe_attribute(key: &str) -> bool {
    let lowered = key.to_ascii_lowercase();
    !FORBIDDEN_ATTRIBUTES
        .iter()
        .any(|forbidden| lowered.contains(forbidden))
}

/// Filtra un conjunto de atributos dejando solo los que pueden registrarse.
///
/// Se usa en los bordes donde los atributos vienen de datos externos y no de
/// literales del código.
pub fn safe_attributes<I, K, V>(attributes: I) -> Vec<(String, String)>
where
    I: IntoIterator<Item = (K, V)>,
    K: AsRef<str>,
    V: Into<String>,
{
    attributes
        .into_iter()
        .filter(|(key, _)| is_safe_attribute(key.as_ref()))
        .map(|(key, value)| (key.as_ref().to_owned(), value.into()))
        .collect()
}

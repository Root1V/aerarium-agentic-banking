"""Guarda de no discriminación.

Un modelo de crédito no puede usar atributos protegidos ni sus sustitutos. No es
solo una exigencia legal: un rasgo protegido que se cuela como característica
produce un sistema que niega crédito por quién es la persona en vez de por cómo
maneja su dinero, y a esa escala el daño es masivo y silencioso.

**Por qué la comparación no es por subcadena.** La primera versión de esta guarda
comparaba subcadenas y bloqueó `average_monthly_inflow_minor`, porque "aver*age*"
contiene "age". Una regla así parece más segura y en realidad es peor: deja el
motor sin las características legítimas que permiten prestarle a quien no tiene
buró, que es justamente el segmento que se busca atender. Los términos cortos se
comparan por palabra completa; los compuestos, sobre el nombre sin separadores.
"""

from __future__ import annotations

import re

#: Términos que por sí solos identifican un rasgo protegido. Se comparan como
#: PALABRA COMPLETA dentro del nombre.
PROTECTED_TERMS = frozenset(
    {
        "gender",
        "sex",
        "race",
        "ethnicity",
        "religion",
        "pregnancy",
        "disability",
        "political",
        "union",
        "age",
        # Sustitutos: correlacionan con rasgos protegidos sin nombrarlos.
        # Discriminar por un dato correlacionado es discriminar igual.
        "nationality",
        "neighborhood",
        "surname",
        "photo",
        "selfie",
    }
)

#: Términos compuestos. Se buscan sobre el nombre con los separadores quitados,
#: para que `date_of_birth` y `dateOfBirth` se detecten igual.
PROTECTED_COMPOUNDS = (
    "maritalstatus",
    "sexualorientation",
    "dateofbirth",
    "birthdate",
    "countryofbirth",
    "placeofbirth",
    "postalcode",
    "zipcode",
    "lastname",
    "familyname",
)


class ProhibitedFeatureError(ValueError):
    """Se intentó usar un atributo protegido en una decisión de crédito."""

    def __init__(self, feature: str) -> None:
        super().__init__(
            f"'{feature}' es un atributo protegido y no puede intervenir en una "
            "decisión de crédito"
        )
        self.feature = feature


def _tokenize(feature_name: str) -> list[str]:
    """Parte un nombre en palabras, tanto en snake_case como en camelCase."""
    spaced = re.sub(r"(?<=[a-z0-9])(?=[A-Z])", "_", feature_name)
    return [token for token in re.split(r"[^a-zA-Z0-9]+", spaced.lower()) if token]


def _compact(feature_name: str) -> str:
    return re.sub(r"[^a-z0-9]", "", feature_name.lower())


def is_prohibited(feature_name: str) -> bool:
    """Indica si un nombre de característica corresponde a un atributo protegido."""
    tokens = set(_tokenize(feature_name))
    if tokens & PROTECTED_TERMS:
        return True

    compact = _compact(feature_name)
    return any(compound in compact for compound in PROTECTED_COMPOUNDS)


def assert_permitted(feature_names: list[str]) -> None:
    """Falla si alguna característica es un atributo protegido.

    Se invoca en la decisión, no solo en las pruebas: la barrera tiene que estar
    en el camino de ejecución para que una característica añadida más adelante no
    entre sin que nadie se dé cuenta.
    """
    for name in feature_names:
        if is_prohibited(name):
            raise ProhibitedFeatureError(name)

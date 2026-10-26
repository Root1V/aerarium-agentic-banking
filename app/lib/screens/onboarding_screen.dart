import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../theme.dart';

/// Alta de nivel 1: DNI, nombres, apellidos y fecha de nacimiento.
///
/// Cuatro campos y ninguno más, a propósito. El alta es el embudo más caro de
/// un banco digital, y abandonarla se dispara con cada campo de más y pasados
/// los tres minutos (doc 14). Lo que no hace falta para abrir una cuenta de
/// topes bajos se pide después, cuando la persona quiera subir de nivel.
///
/// Qué pasa al aprobarse lo decide quien monta la pantalla: hoy, en el sandbox,
/// entrar con una sesión de prueba; mañana, registrar la passkey del
/// dispositivo. Esta pantalla no sabe de sesiones.
class OnboardingScreen extends StatefulWidget {
  const OnboardingScreen({
    super.key,
    required this.client,
    required this.onApproved,
    DateTime Function()? now,
    String Function()? newReference,
  })  : _now = now ?? DateTime.now,
        _newReference = newReference ?? _uuidV4;

  final BffClient client;
  final void Function(OnboardingResult result) onApproved;
  final DateTime Function() _now;
  final String Function() _newReference;

  @override
  State<OnboardingScreen> createState() => _OnboardingScreenState();
}

enum _Phase { form, inReview, rejected }

class _OnboardingScreenState extends State<OnboardingScreen> {
  final _dni = TextEditingController();
  final _firstNames = TextEditingController();
  final _lastNames = TextEditingController();
  final _birth = TextEditingController();

  /// La referencia del alta. Se genera UNA vez y se conserva en cada reintento:
  /// si la respuesta se pierde por la red y la persona vuelve a pulsar, el banco
  /// tiene que reconocer que es el mismo alta y no abrir una segunda cuenta.
  late String _reference = widget._newReference();

  _Phase _phase = _Phase.form;
  bool _busy = false;
  String? _banner;
  String? _stillInReview;
  final _fieldErrors = <String, String>{};

  @override
  void dispose() {
    for (final c in [_dni, _firstNames, _lastNames, _birth]) {
      c.dispose();
    }
    super.dispose();
  }

  /// Comprueba lo mismo que el servidor, para avisar en el campo antes de
  /// enviar. La verdad sigue estando en el servidor.
  OnboardingRequest? _validate() {
    final errors = <String, String>{};

    final dni = _dni.text.trim();
    if (!RegExp(r'^[0-9]{8}$').hasMatch(dni)) {
      errors['dni'] = 'El DNI tiene 8 dígitos.';
    }
    final first = _firstNames.text.trim();
    final last = _lastNames.text.trim();
    if (first.isEmpty) errors['first'] = 'Escribe tus nombres.';
    if (last.isEmpty) errors['last'] = 'Escribe tus apellidos.';

    final birth = parseBirthDate(_birth.text);
    if (birth == null) {
      errors['birth'] = 'Escribe la fecha como DD/MM/AAAA.';
    } else if (ageOn(birth, widget._now()) < 18) {
      errors['birth'] = 'Necesitas ser mayor de edad para abrir una cuenta.';
    }

    setState(() {
      _fieldErrors
        ..clear()
        ..addAll(errors);
    });
    if (errors.isNotEmpty) return null;

    return OnboardingRequest(
      reference: _reference,
      documentNumber: dni,
      firstNames: first,
      lastNames: last,
      dateOfBirth: birth!,
    );
  }

  Future<void> _submit() async {
    final request = _validate();
    if (request == null) return;

    setState(() {
      _busy = true;
      _banner = null;
    });

    try {
      final result = await widget.client.submitOnboarding(request);
      if (!mounted) return;
      _show(result);
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        switch (e.code) {
          case ApiErrorCode.invalidDocument:
            _fieldErrors['dni'] = e.userMessage;
          case ApiErrorCode.invalidName:
            _fieldErrors['first'] = e.userMessage;
          case ApiErrorCode.invalidDate || ApiErrorCode.underage:
            _fieldErrors['birth'] = e.userMessage;
          case ApiErrorCode.referenceConflict:
            // La referencia se usó con otros datos: se descarta y la próxima
            // vez sale una nueva. Es un caso de laboratorio, no de uso normal.
            _reference = widget._newReference();
            _banner = e.userMessage;
          default:
            // Red, proveedor caído, límite: la referencia se CONSERVA para que
            // el reintento retome el mismo alta.
            _banner = e.userMessage;
        }
      });
    }
  }

  void _show(OnboardingResult result) {
    switch (result.status) {
      case OnboardingStatus.approved:
        setState(() => _busy = false);
        widget.onApproved(result);
      case OnboardingStatus.inReview:
        setState(() {
          _busy = false;
          _phase = _Phase.inReview;
        });
      case OnboardingStatus.rejected:
        setState(() {
          _busy = false;
          _phase = _Phase.rejected;
        });
      case OnboardingStatus.inProgress:
        setState(() {
          _busy = false;
          _banner = 'No pudimos terminar la verificación. Vuelve a intentarlo.';
        });
    }
  }

  Future<void> _checkAgain() async {
    setState(() {
      _busy = true;
      _stillInReview = null;
    });
    try {
      final result = await widget.client.onboardingStatus(_reference);
      if (!mounted) return;
      if (result.status == OnboardingStatus.inReview) {
        setState(() {
          _busy = false;
          _stillInReview = 'Sigue en revisión. Vuelve a consultar más tarde.';
        });
        return;
      }
      _show(result);
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _stillInReview = e.userMessage;
      });
    }
  }

  void _startOver() {
    setState(() {
      // El alta rechazada está cerrada. Volver a intentarlo es un alta nueva,
      // con referencia nueva; los datos escritos se conservan para corregirlos.
      _reference = widget._newReference();
      _phase = _Phase.form;
      _banner = null;
    });
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('Abre tu cuenta')),
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 480),
            child: switch (_phase) {
              _Phase.form => _form(context),
              _Phase.inReview => _inReview(context),
              _Phase.rejected => _rejected(context),
            },
          ),
        ),
      ),
    );
  }

  Widget _form(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodyMedium?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return ListView(
      padding: const EdgeInsets.all(AppSpacing.xl),
      children: [
        // Decir cuánto falta baja el abandono: la gente deja a medias lo que
        // no sabe cuánto va a durar.
        Text('Te toma menos de 2 minutos. Solo necesitas tu DNI.', style: muted),
        const SizedBox(height: AppSpacing.xl),
        TextField(
          key: const Key('dni'),
          controller: _dni,
          enabled: !_busy,
          keyboardType: TextInputType.number,
          inputFormatters: [
            FilteringTextInputFormatter.digitsOnly,
            LengthLimitingTextInputFormatter(8),
          ],
          decoration: InputDecoration(labelText: 'DNI', errorText: _fieldErrors['dni']),
        ),
        const SizedBox(height: AppSpacing.lg),
        TextField(
          key: const Key('first'),
          controller: _firstNames,
          enabled: !_busy,
          textCapitalization: TextCapitalization.words,
          decoration: InputDecoration(labelText: 'Nombres', errorText: _fieldErrors['first']),
        ),
        const SizedBox(height: AppSpacing.lg),
        TextField(
          key: const Key('last'),
          controller: _lastNames,
          enabled: !_busy,
          textCapitalization: TextCapitalization.words,
          decoration: InputDecoration(labelText: 'Apellidos', errorText: _fieldErrors['last']),
        ),
        const SizedBox(height: AppSpacing.lg),
        TextField(
          key: const Key('birth'),
          controller: _birth,
          enabled: !_busy,
          // Teclado numérico y las barras se ponen solas: el teclado numérico
          // de iOS no tiene "/", y pedirlo dejaría a la persona sin forma de
          // escribir la fecha.
          keyboardType: TextInputType.number,
          inputFormatters: [BirthDateFormatter()],
          decoration: InputDecoration(
            labelText: 'Fecha de nacimiento',
            hintText: 'DD/MM/AAAA',
            errorText: _fieldErrors['birth'],
          ),
        ),
        const SizedBox(height: AppSpacing.lg),
        Text(
          'Usamos estos datos solo para verificar tu identidad con la fuente oficial.',
          style: theme.textTheme.bodySmall?.copyWith(color: theme.colorScheme.onSurfaceVariant),
        ),
        if (_banner != null) ...[
          const SizedBox(height: AppSpacing.lg),
          Text(_banner!, style: TextStyle(color: theme.colorScheme.error)),
        ],
        const SizedBox(height: AppSpacing.xl),
        FilledButton(
          onPressed: _busy ? null : _submit,
          child: _busy
              ? const SizedBox.square(
                  dimension: 20,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Abrir mi cuenta'),
        ),
      ],
    );
  }

  /// La misma pantalla para cualquier motivo de revisión.
  ///
  /// No se distingue una foto borrosa de una coincidencia en listas de
  /// sanciones: revelar lo segundo está prohibido por la normativa contra el
  /// lavado. Tampoco se promete un plazo ni un aviso que el banco todavía no
  /// sabe enviar.
  Widget _inReview(BuildContext context) {
    final theme = Theme.of(context);
    return ListView(
      padding: const EdgeInsets.all(AppSpacing.xl),
      children: [
        Icon(Icons.hourglass_top, size: 48, color: theme.colorScheme.secondary),
        const SizedBox(height: AppSpacing.lg),
        Text('Estamos revisando tus datos', style: theme.textTheme.titleLarge),
        const SizedBox(height: AppSpacing.md),
        Text(
          'A veces la verificación necesita que una persona la mire. '
          'Vuelve a consultar más tarde.',
          style: theme.textTheme.bodyMedium,
        ),
        if (_stillInReview != null) ...[
          const SizedBox(height: AppSpacing.lg),
          Text(_stillInReview!, style: theme.textTheme.bodyMedium),
        ],
        const SizedBox(height: AppSpacing.xl),
        OutlinedButton(
          onPressed: _busy ? null : _checkAgain,
          child: const Text('Consultar de nuevo'),
        ),
      ],
    );
  }

  Widget _rejected(BuildContext context) {
    final theme = Theme.of(context);
    return ListView(
      padding: const EdgeInsets.all(AppSpacing.xl),
      children: [
        Text('No pudimos abrir tu cuenta con estos datos', style: theme.textTheme.titleLarge),
        const SizedBox(height: AppSpacing.md),
        Text(
          'Revisa que coincidan exactamente con tu DNI y vuelve a intentarlo.',
          style: theme.textTheme.bodyMedium,
        ),
        const SizedBox(height: AppSpacing.xl),
        FilledButton(onPressed: _startOver, child: const Text('Revisar mis datos')),
      ],
    );
  }
}

/// Lee una fecha DD/MM/AAAA. Devuelve null si no es una fecha que exista:
/// "30/02/1995" no es una fecha, aunque tenga la forma de una.
DateTime? parseBirthDate(String text) {
  final match = RegExp(r'^(\d{2})/(\d{2})/(\d{4})$').firstMatch(text.trim());
  if (match == null) return null;
  final day = int.parse(match.group(1)!);
  final month = int.parse(match.group(2)!);
  final year = int.parse(match.group(3)!);
  if (year < 1900) return null;
  final date = DateTime(year, month, day);
  if (date.year != year || date.month != month || date.day != day) return null;
  return date;
}

/// Edad cumplida en una fecha.
int ageOn(DateTime birth, DateTime at) {
  var age = at.year - birth.year;
  if (at.month < birth.month || (at.month == birth.month && at.day < birth.day)) {
    age--;
  }
  return age;
}

/// Escribe la fecha con el teclado numérico: pone las barras sola y no deja
/// pasar de ocho dígitos.
class BirthDateFormatter extends TextInputFormatter {
  @override
  TextEditingValue formatEditUpdate(TextEditingValue oldValue, TextEditingValue newValue) {
    final digits = newValue.text.replaceAll(RegExp(r'\D'), '');
    final clipped = digits.length > 8 ? digits.substring(0, 8) : digits;
    final buffer = StringBuffer();
    for (var i = 0; i < clipped.length; i++) {
      if (i == 2 || i == 4) buffer.write('/');
      buffer.write(clipped[i]);
    }
    final text = buffer.toString();
    return TextEditingValue(
      text: text,
      selection: TextSelection.collapsed(offset: text.length),
    );
  }
}

/// UUID v4 con un generador criptográfico.
///
/// La referencia del alta funciona como un token —quien la tiene puede consultar
/// el alta—, así que no puede salir de un generador predecible.
String _uuidV4() {
  final random = Random.secure();
  final bytes = List<int>.generate(16, (_) => random.nextInt(256));
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // versión 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variante RFC 4122
  String hex(int from, int to) =>
      bytes.sublist(from, to).map((b) => b.toRadixString(16).padLeft(2, '0')).join();
  return '${hex(0, 4)}-${hex(4, 6)}-${hex(6, 8)}-${hex(8, 10)}-${hex(10, 16)}';
}

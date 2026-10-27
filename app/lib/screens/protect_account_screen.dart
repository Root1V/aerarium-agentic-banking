import 'package:flutter/material.dart';

import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../theme.dart';
import '../widgets/pin_pad.dart';

/// "Protege tu cuenta": vincula este teléfono a la cuenta.
///
/// Dos pasos. Primero la clave de 6 dígitos, que es el respaldo para cuando la
/// cara o la huella fallen. Después Face ID, que crea las llaves en el chip
/// seguro del teléfono. Desde ahí, entrar es mirar la cámara.
///
/// No tiene botón de "más tarde": sin este paso no hay forma de volver a entrar
/// a la cuenta.
class ProtectAccountScreen extends StatefulWidget {
  const ProtectAccountScreen({
    super.key,
    required this.flow,
    required this.biometryName,
    required this.onLinked,
    this.onboardingReference,
  });

  final DeviceFlow flow;
  final String biometryName;

  /// La referencia del alta, para el primer teléfono. Sin ella, el cliente del
  /// flujo lleva la sesión de quien entró con su passkey en un teléfono nuevo.
  final String? onboardingReference;

  final void Function(SessionGrant session) onLinked;

  @override
  State<ProtectAccountScreen> createState() => _ProtectAccountScreenState();
}

enum _Step { choosePin, confirmPin, biometrics }

class _ProtectAccountScreenState extends State<ProtectAccountScreen> {
  _Step _step = _Step.choosePin;
  String _pin = '';
  bool _busy = false;
  String? _message;

  void _chosen(String pin) {
    if (isWeakPin(pin)) {
      setState(() => _message = ApiError(ApiErrorCode.weakPin, '').userMessage);
      return;
    }
    setState(() {
      _pin = pin;
      _step = _Step.confirmPin;
      _message = null;
    });
  }

  void _confirmed(String pin) {
    if (pin != _pin) {
      setState(() {
        _step = _Step.choosePin;
        _message = 'Las claves no coinciden. Elige tu clave otra vez.';
      });
      return;
    }
    setState(() {
      _step = _Step.biometrics;
      _message = null;
    });
  }

  Future<void> _link() async {
    setState(() {
      _busy = true;
      _message = null;
    });
    try {
      final session = await widget.flow.link(
        reference: widget.onboardingReference,
        pin: _pin,
        biometryName: widget.biometryName,
      );
      if (!mounted) return;
      setState(() => _busy = false);
      widget.onLinked(session);
    } catch (e) {
      // La causa técnica va a la consola, nunca a la pantalla.
      debugPrint('vincular el teléfono: $e');
      if (!mounted) return;
      setState(() {
        _busy = false;
        _message = explainDeviceError(e, biometryName: widget.biometryName);
        // El banco rechazó la clave: se vuelve a elegir, sin repetir la cara aún.
        if (e is ApiError && e.code == ApiErrorCode.weakPin) _step = _Step.choosePin;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final (title, body) = switch (_step) {
      _Step.choosePin => ('Crea tu clave', 'Seis dígitos. La usarás si ${widget.biometryName} no te reconoce.'),
      _Step.confirmPin => ('Confirma tu clave', 'Escríbela otra vez.'),
      _Step.biometrics => (
          'Activa ${widget.biometryName}',
          'Así entrarás a tu cuenta con solo mirar el teléfono. Tu cara se queda '
              'en el teléfono: el banco no la recibe.'
        ),
    };

    return Scaffold(
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 440),
            child: ListView(
              padding: const EdgeInsets.all(AppSpacing.xl),
              shrinkWrap: true,
              children: [
                Icon(Icons.verified_user_outlined, size: 40, color: theme.colorScheme.primary),
                const SizedBox(height: AppSpacing.md),
                Text('Protege tu cuenta', style: theme.textTheme.labelLarge?.copyWith(
                  color: theme.colorScheme.primary,
                )),
                const SizedBox(height: AppSpacing.sm),
                Text(title, style: theme.textTheme.headlineMedium),
                const SizedBox(height: AppSpacing.sm),
                Text(body, style: theme.textTheme.bodyLarge?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                )),
                const SizedBox(height: AppSpacing.xl),
                if (_message != null) ...[
                  Text(_message!, style: TextStyle(color: theme.colorScheme.error)),
                  const SizedBox(height: AppSpacing.lg),
                ],
                switch (_step) {
                  _Step.choosePin => PinPad(key: const ValueKey('choose'), onCompleted: _chosen),
                  _Step.confirmPin => PinPad(key: const ValueKey('confirm'), onCompleted: _confirmed),
                  _Step.biometrics => FilledButton.icon(
                      onPressed: _busy ? null : _link,
                      icon: _busy
                          ? const SizedBox.square(
                              dimension: 20,
                              child: CircularProgressIndicator(strokeWidth: 2),
                            )
                          : const Icon(Icons.face_unlock_outlined),
                      label: Text('Activar ${widget.biometryName}'),
                    ),
                },
              ],
            ),
          ),
        ),
      ),
    );
  }
}

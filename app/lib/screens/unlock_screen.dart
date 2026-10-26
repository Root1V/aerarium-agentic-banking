import 'package:flutter/material.dart';

import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../theme.dart';
import '../widgets/pin_pad.dart';

/// Lo que ve quien abre la app en su teléfono vinculado: nada que escribir.
///
/// Face ID se pide solo al aparecer la pantalla. Si falla, se cancela o la
/// persona prefiere, está la clave de 6 dígitos.
class UnlockScreen extends StatefulWidget {
  const UnlockScreen({
    super.key,
    required this.flow,
    required this.deviceId,
    required this.biometryName,
    required this.onUnlocked,
    required this.onUnlinked,
    this.autoPrompt = true,
  });

  /// Pedir la cara en cuanto aparece la pantalla.
  final bool autoPrompt;

  final DeviceFlow flow;
  final String deviceId;
  final String biometryName;
  final void Function(SessionGrant session) onUnlocked;

  /// El banco ya no reconoce este teléfono —se desvinculó o se bloqueó—: las
  /// llaves se borraron y toca entrar de otra forma. Recibe qué decirle.
  final void Function(String message) onUnlinked;

  @override
  State<UnlockScreen> createState() => _UnlockScreenState();
}

class _UnlockScreenState extends State<UnlockScreen> {
  bool _busy = false;
  bool _usePin = false;
  String? _message;

  @override
  void initState() {
    super.initState();
    if (widget.autoPrompt) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _unlock(DeviceUnlock.biometric));
    }
  }

  Future<void> _unlock(DeviceUnlock method, {String? pin}) async {
    if (_busy) return;
    setState(() {
      _busy = true;
      _message = null;
    });
    try {
      final session = await widget.flow.unlock(widget.deviceId, method, pin: pin);
      if (!mounted) return;
      setState(() => _busy = false);
      widget.onUnlocked(session);
    } catch (e) {
      // La causa técnica va a la consola, nunca a la pantalla.
      debugPrint('entrar con el teléfono: $e');
      if (!mounted) return;
      if (e is ApiError && (e.code == ApiErrorCode.deviceRejected || e.code == ApiErrorCode.deviceLocked)) {
        // El banco ya no deja entrar con este teléfono: sus llaves no sirven y
        // se borran, para no insistir con ellas en cada arranque.
        await widget.flow.keys.reset();
        widget.onUnlinked(e.userMessage);
        return;
      }
      setState(() {
        _busy = false;
        _message = explainDeviceError(e, biometryName: widget.biometryName);
        // Si la biometría no puede, se pasa a la clave sin pedirlo.
        if (e is DeviceKeyException && e.failure != DeviceKeyFailure.cancelled) _usePin = true;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Scaffold(
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 440),
            child: ListView(
              padding: const EdgeInsets.all(AppSpacing.xl),
              shrinkWrap: true,
              children: [
                Text('AIBank', textAlign: TextAlign.center, style: theme.textTheme.displaySmall?.copyWith(
                  color: theme.colorScheme.primary,
                  fontWeight: FontWeight.w700,
                )),
                const SizedBox(height: AppSpacing.sm),
                Text(
                  _usePin ? 'Escribe tu clave' : 'Hola de nuevo',
                  textAlign: TextAlign.center,
                  style: theme.textTheme.titleMedium,
                ),
                const SizedBox(height: AppSpacing.xl),
                if (_message != null) ...[
                  Text(_message!, textAlign: TextAlign.center, style: TextStyle(color: theme.colorScheme.error)),
                  const SizedBox(height: AppSpacing.lg),
                ],
                if (_usePin)
                  PinPad(
                    enabled: !_busy,
                    onCompleted: (pin) => _unlock(DeviceUnlock.pin, pin: pin),
                    extraKey: IconButton(
                      tooltip: 'Usar ${widget.biometryName}',
                      icon: const Icon(Icons.face_unlock_outlined),
                      onPressed: _busy ? null : () => _unlock(DeviceUnlock.biometric),
                    ),
                  )
                else ...[
                  FilledButton.icon(
                    onPressed: _busy ? null : () => _unlock(DeviceUnlock.biometric),
                    icon: _busy
                        ? const SizedBox.square(
                            dimension: 20,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          )
                        : const Icon(Icons.face_unlock_outlined),
                    label: Text('Entrar con ${widget.biometryName}'),
                  ),
                  const SizedBox(height: AppSpacing.md),
                  TextButton(
                    onPressed: _busy ? null : () => setState(() => _usePin = true),
                    child: const Text('Usar mi clave'),
                  ),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

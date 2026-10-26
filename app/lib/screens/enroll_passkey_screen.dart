import 'package:flutter/material.dart';

import '../api/errors.dart';
import '../api/models.dart';
import '../auth/passkeys.dart';
import '../theme.dart';

/// "Protege tu cuenta": la primera passkey, justo después del alta aprobada.
///
/// No tiene botón de "más tarde". Sin passkey no hay forma de volver a entrar a
/// la cuenta, así que saltarse este paso dejaría a la persona fuera de su
/// propio dinero. El banco acepta la referencia del alta para esto durante 24
/// horas y solo una vez; después, recuperar el acceso pasa por soporte.
class EnrollPasskeyScreen extends StatefulWidget {
  const EnrollPasskeyScreen({
    super.key,
    required this.flow,
    required this.onboardingReference,
    required this.onEnrolled,
    required this.onAlreadyEnrolled,
  });

  final PasskeyFlow flow;
  final String onboardingReference;
  final void Function(SessionGrant session) onEnrolled;

  /// La cuenta ya tiene passkey —por ejemplo, la respuesta del registro se
  /// perdió y la persona vuelve a intentarlo—: lo que toca es entrar con ella.
  final VoidCallback onAlreadyEnrolled;

  @override
  State<EnrollPasskeyScreen> createState() => _EnrollPasskeyScreenState();
}

class _EnrollPasskeyScreenState extends State<EnrollPasskeyScreen> {
  bool _busy = false;
  String? _error;
  bool _alreadyEnrolled = false;

  Future<void> _enroll() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final session = await widget.flow.enroll(widget.onboardingReference);
      if (!mounted) return;
      setState(() => _busy = false);
      widget.onEnrolled(session);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _alreadyEnrolled = e is ApiError && e.code == ApiErrorCode.alreadyEnrolled;
        _error = explainPasskeyError(e);
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
                Icon(Icons.verified_user_outlined, size: 48, color: theme.colorScheme.primary),
                const SizedBox(height: AppSpacing.lg),
                Text('Protege tu cuenta', style: theme.textTheme.headlineMedium),
                const SizedBox(height: AppSpacing.md),
                Text(
                  'Tu cuenta ya está abierta. Para entrar usarás una passkey: se '
                  'guarda en este teléfono y se desbloquea con tu cara o tu huella. '
                  'No hay contraseña que olvidar ni que te puedan robar.',
                  style: theme.textTheme.bodyLarge?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                const SizedBox(height: AppSpacing.xxl),
                if (_alreadyEnrolled)
                  FilledButton(
                    onPressed: widget.onAlreadyEnrolled,
                    child: const Text('Entrar con mi passkey'),
                  )
                else
                  FilledButton.icon(
                    onPressed: _busy ? null : _enroll,
                    icon: _busy
                        ? const SizedBox.square(
                            dimension: 20,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          )
                        : const Icon(Icons.fingerprint),
                    label: const Text('Crear passkey'),
                  ),
                if (_error != null) ...[
                  const SizedBox(height: AppSpacing.md),
                  Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }
}

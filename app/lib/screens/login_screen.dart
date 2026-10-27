import 'package:flutter/material.dart';

import '../api/models.dart';
import '../auth/passkeys.dart';
import '../theme.dart';

/// Entrar al banco con la passkey del teléfono.
///
/// Un solo botón. No hay usuario que escribir: el sistema ofrece las passkeys
/// guardadas para el dominio del banco y la elegida dice de quién es la cuenta.
/// Tampoco hay contraseña ni código por SMS que se puedan robar o reenviar a
/// una web falsa: la firma solo vale para el dominio del banco (doc 14).
class LoginScreen extends StatefulWidget {
  const LoginScreen({
    super.key,
    required this.flow,
    required this.onSignedIn,
    this.onOpenAccount,
    this.notice,
  });

  /// Algo que decir al llegar, por ejemplo que el teléfono se desvinculó.
  final String? notice;

  final PasskeyFlow flow;
  final void Function(SessionGrant session) onSignedIn;

  /// Lleva al alta. Opcional: quien monta la pantalla decide si se ofrece.
  final VoidCallback? onOpenAccount;

  @override
  State<LoginScreen> createState() => _LoginScreenState();
}

class _LoginScreenState extends State<LoginScreen> {
  bool _busy = false;
  String? _error;

  Future<void> _signIn() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      final session = await widget.flow.login();
      if (!mounted) return;
      setState(() => _busy = false);
      widget.onSignedIn(session);
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
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
                Text('AIBank', style: theme.textTheme.displaySmall?.copyWith(
                  color: theme.colorScheme.primary,
                  fontWeight: FontWeight.w700,
                )),
                const SizedBox(height: AppSpacing.sm),
                Text(
                  'Entra con tu passkey: tu cara o tu huella, sin contraseñas ni códigos.',
                  style: theme.textTheme.bodyLarge?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                if (widget.notice != null) ...[
                  const SizedBox(height: AppSpacing.lg),
                  Text(widget.notice!, style: theme.textTheme.bodyMedium),
                ],
                const SizedBox(height: AppSpacing.xxl),
                FilledButton.icon(
                  onPressed: _busy ? null : _signIn,
                  icon: _busy
                      ? const SizedBox.square(
                          dimension: 20,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Icon(Icons.fingerprint),
                  label: const Text('Entrar'),
                ),
                if (_error != null) ...[
                  const SizedBox(height: AppSpacing.md),
                  Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
                ],
                if (widget.onOpenAccount != null) ...[
                  const SizedBox(height: AppSpacing.xxl),
                  const Divider(),
                  const SizedBox(height: AppSpacing.lg),
                  Text(
                    '¿Todavía no tienes cuenta? Ábrela en unos minutos con tu DNI.',
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: AppSpacing.md),
                  OutlinedButton(
                    onPressed: _busy ? null : widget.onOpenAccount,
                    child: const Text('Abrir una cuenta'),
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

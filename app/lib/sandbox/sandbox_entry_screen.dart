import 'package:flutter/material.dart';

import '../money.dart';
import '../theme.dart';
import 'sandbox_client.dart';

/// Puerta de entrada del sandbox: crea una persona de prueba y entra con ella.
///
/// No es un login. No pide credenciales ni las comprueba: le pide al simulador
/// que invente un titular con saldo. Por eso solo se compila con
/// `--dart-define=AIBANK_SANDBOX=true`, y sin ese valor el compilador la
/// elimina de la app — no queda desactivada, deja de existir.
///
/// El login real son passkeys (doc 14, regla 1): `LoginScreen`.
class SandboxEntryScreen extends StatefulWidget {
  const SandboxEntryScreen({
    super.key,
    required this.client,
    required this.onSession,
    this.onOpenAccount,
  });

  final SandboxClient client;

  /// Qué hacer con la sesión creada. La decide quien monta la pantalla, para
  /// que este archivo no tenga que saber cómo se construye el resto de la app.
  final void Function(SandboxSession session) onSession;

  /// Lleva al alta real. Opcional: sin él la pantalla solo ofrece el titular de
  /// prueba, que es lo que hacía siempre.
  final VoidCallback? onOpenAccount;

  @override
  State<SandboxEntryScreen> createState() => _SandboxEntryScreenState();
}

class _SandboxEntryScreenState extends State<SandboxEntryScreen> {
  /// Saldo con el que nace el titular: suficiente para probar transferencias y
  /// pagos de agentes sin quedarse corto a la tercera prueba.
  static const _initialBalance = 100 * microsPerUnit;

  final _name = TextEditingController(text: 'Ana Pérez');
  bool _busy = false;
  String? _error;

  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  Future<void> _create() async {
    final name = _name.text.trim();
    if (name.isEmpty) {
      setState(() => _error = 'Escribe un nombre para el titular de prueba.');
      return;
    }

    setState(() {
      _busy = true;
      _error = null;
    });

    try {
      final session = await widget.client.createHolder(
        name: name,
        initialBalanceMicros: _initialBalance,
      );
      if (!mounted) return;
      // El spinner se apaga aunque quien recibe la sesión no navegue a otra
      // pantalla: dejarlo girando confiaría en algo que este archivo no
      // controla, y si falla, la pantalla se queda colgada para siempre.
      setState(() => _busy = false);
      widget.onSession(session);
    } on SandboxException catch (e) {
      if (!mounted) return;
      setState(() {
        _busy = false;
        _error = _explain(e.failure);
      });
    }
  }

  String _explain(SandboxFailure failure) => switch (failure) {
        SandboxFailure.unreachable =>
          'No hay respuesta en ${widget.client.baseUrl}. ¿Está levantado el '
              'sandbox? Ver SANDBOX.md.',
        SandboxFailure.wrongDevKey =>
          'Este entorno pide una clave de pruebas y la que tiene la app no '
              'coincide. Compila con --dart-define=AIBANK_DEV_KEY=...',
        SandboxFailure.notASandbox =>
          'El servidor responde, pero no es un sandbox: arrancó sin '
              'ALLOW_DEV_AUTH.',
        SandboxFailure.unknown =>
          'Algo salió mal al crear el titular. Inténtalo otra vez.',
      };

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);

    return Scaffold(
      body: SafeArea(
        child: Center(
          child: ConstrainedBox(
            // En el portal web la pantalla puede medir 1.900 px de ancho; un
            // formulario estirado a todo lo ancho se lee peor.
            constraints: const BoxConstraints(maxWidth: 440),
            child: ListView(
              padding: const EdgeInsets.all(AppSpacing.xl),
              shrinkWrap: true,
              children: [
                const _SandboxBanner(),
                const SizedBox(height: AppSpacing.xxl),
                Text('AIBank', style: theme.textTheme.displaySmall?.copyWith(
                  color: theme.colorScheme.primary,
                  fontWeight: FontWeight.w700,
                )),
                const SizedBox(height: AppSpacing.sm),
                Text(
                  'Crea una persona de prueba con una cuenta y '
                  '${const Money(_initialBalance, 'USD').format()} para recorrer la app.',
                  style: theme.textTheme.bodyLarge?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                const SizedBox(height: AppSpacing.xl),
                TextField(
                  controller: _name,
                  enabled: !_busy,
                  textCapitalization: TextCapitalization.words,
                  decoration: const InputDecoration(labelText: 'Nombre'),
                  onSubmitted: (_) => _create(),
                ),
                if (_error != null) ...[
                  const SizedBox(height: AppSpacing.md),
                  Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
                ],
                const SizedBox(height: AppSpacing.xl),
                FilledButton(
                  onPressed: _busy ? null : _create,
                  child: _busy
                      ? const SizedBox.square(
                          dimension: 20,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : const Text('Crear titular de prueba'),
                ),
                if (widget.onOpenAccount != null) ...[
                  const SizedBox(height: AppSpacing.xxl),
                  const Divider(),
                  const SizedBox(height: AppSpacing.lg),
                  Text(
                    'O recorre el alta de verdad: DNI, verificación de identidad '
                    'y cuenta básica.',
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

/// Aviso permanente de que nada de esto es dinero real.
///
/// Va arriba y en el color de la marca, no en letra pequeña: quien vea una
/// captura de esta pantalla tiene que saber de un vistazo que es una prueba.
class _SandboxBanner extends StatelessWidget {
  const _SandboxBanner();

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(
        horizontal: AppSpacing.lg,
        vertical: AppSpacing.md,
      ),
      decoration: BoxDecoration(
        color: scheme.secondary,
        borderRadius: BorderRadius.circular(AppRadius.md),
      ),
      child: Row(
        children: [
          Icon(Icons.science_outlined, color: scheme.onSecondary),
          const SizedBox(width: AppSpacing.md),
          Expanded(
            child: Text(
              'Entorno de pruebas · sin dinero real',
              style: TextStyle(
                color: scheme.onSecondary,
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
        ],
      ),
    );
  }
}

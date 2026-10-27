import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../theme.dart';

/// "Mi número para recibir": registrar el celular propio para que te envíen
/// dinero con solo tu número.
///
/// Registrarlo exige probar que es tuyo con un código por SMS. En el sandbox no
/// hay SMS: el código se muestra en pantalla, marcado como de prueba.
class PhoneAliasScreen extends StatefulWidget {
  const PhoneAliasScreen({super.key, required this.client, required this.accountId});

  final BffClient client;
  final String accountId;

  @override
  State<PhoneAliasScreen> createState() => _PhoneAliasScreenState();
}

class _PhoneAliasScreenState extends State<PhoneAliasScreen> {
  final _phone = TextEditingController();
  final _code = TextEditingController();

  bool _loading = true;
  bool _busy = false;
  String? _registered;
  PhoneVerification? _pending;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _phone.dispose();
    _code.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final phone = await widget.client.myPhone();
      if (!mounted) return;
      setState(() {
        _registered = phone;
        _loading = false;
      });
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _loading = false;
        _error = e.code == ApiErrorCode.unknown
            ? 'Recibir con tu número todavía no está disponible.'
            : e.userMessage;
      });
    }
  }

  Future<void> _run(Future<void> Function() action) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await action();
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.userMessage;
        // Un código agotado o vencido no se puede seguir intentando.
        if (e.code == ApiErrorCode.codeLocked || e.code == ApiErrorCode.ceremonyExpired) _pending = null;
      });
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _sendCode() => _run(() async {
        final pending = await widget.client.startPhoneAlias(widget.accountId, _phone.text);
        if (mounted) setState(() => _pending = pending);
      });

  Future<void> _verify() => _run(() async {
        final phone = await widget.client.verifyPhoneAlias(_pending!.id, _code.text.trim());
        if (!mounted) return;
        setState(() {
          _registered = phone;
          _pending = null;
          _code.clear();
        });
      });

  Future<void> _remove() => _run(() async {
        await widget.client.removePhoneAlias();
        if (mounted) setState(() => _registered = null);
      });

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodyLarge?.copyWith(color: theme.colorScheme.onSurfaceVariant);

    final List<Widget> body;
    if (_loading) {
      body = [const Center(child: CircularProgressIndicator())];
    } else if (_registered case final phone?) {
      body = [
        Icon(Icons.phone_iphone, size: 40, color: theme.colorScheme.primary),
        const SizedBox(height: AppSpacing.md),
        Text('Recibes en ${formatPhone(phone)}', style: theme.textTheme.titleLarge),
        const SizedBox(height: AppSpacing.sm),
        Text('Quien te envíe a este número verá tu nombre y la inicial de tu apellido.', style: muted),
        const SizedBox(height: AppSpacing.xl),
        OutlinedButton(onPressed: _busy ? null : _remove, child: const Text('Quitar mi número')),
      ];
    } else if (_pending case final pending?) {
      body = [
        Text('Escribe el código', style: theme.textTheme.titleLarge),
        const SizedBox(height: AppSpacing.sm),
        Text('Te lo enviamos por SMS al ${formatPhone(pending.phone)}.', style: muted),
        if (pending.sandboxCode case final code?) ...[
          const SizedBox(height: AppSpacing.md),
          _SandboxCode(code: code),
        ],
        const SizedBox(height: AppSpacing.lg),
        TextField(
          key: const Key('code'),
          controller: _code,
          enabled: !_busy,
          keyboardType: TextInputType.number,
          autofillHints: const [AutofillHints.oneTimeCode],
          maxLength: 6,
          decoration: const InputDecoration(labelText: 'Código de 6 dígitos'),
          onSubmitted: (_) => _verify(),
        ),
        const SizedBox(height: AppSpacing.md),
        FilledButton(onPressed: _busy ? null : _verify, child: const Text('Verificar')),
        TextButton(
          onPressed: _busy ? null : () => setState(() => _pending = null),
          child: const Text('Usar otro número'),
        ),
      ];
    } else {
      body = [
        Text('Recibe con tu número', style: theme.textTheme.titleLarge),
        const SizedBox(height: AppSpacing.sm),
        Text(
          'Registra tu celular y cualquiera podrá enviarte dinero con solo tu número. '
          'Te mandaremos un código para comprobar que es tuyo.',
          style: muted,
        ),
        const SizedBox(height: AppSpacing.lg),
        TextField(
          key: const Key('phone'),
          controller: _phone,
          enabled: !_busy,
          keyboardType: TextInputType.phone,
          autofillHints: const [AutofillHints.telephoneNumber],
          decoration: const InputDecoration(labelText: 'Celular', prefixText: '+51 '),
          onSubmitted: (_) => _sendCode(),
        ),
        const SizedBox(height: AppSpacing.md),
        FilledButton(onPressed: _busy ? null : _sendCode, child: const Text('Enviarme un código')),
      ];
    }

    return Scaffold(
      appBar: AppBar(title: const Text('Mi número para recibir')),
      body: ListView(
        padding: const EdgeInsets.all(AppSpacing.xl),
        children: [
          ...body,
          if (_error != null) ...[
            const SizedBox(height: AppSpacing.md),
            Text(_error!, style: TextStyle(color: theme.colorScheme.error)),
          ],
        ],
      ),
    );
  }
}

/// El código "enviado", en el sandbox. Bien marcado: no es un SMS de verdad.
class _SandboxCode extends StatelessWidget {
  const _SandboxCode({required this.code});

  final String code;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.all(AppSpacing.md),
      decoration: BoxDecoration(
        color: scheme.secondaryContainer,
        borderRadius: BorderRadius.circular(AppRadius.md),
      ),
      child: Row(
        children: [
          Icon(Icons.science_outlined, color: scheme.onSecondaryContainer),
          const SizedBox(width: AppSpacing.md),
          Expanded(
            child: Text('Entorno de pruebas, sin SMS. Tu código es $code',
                style: TextStyle(color: scheme.onSecondaryContainer)),
          ),
        ],
      ),
    );
  }
}

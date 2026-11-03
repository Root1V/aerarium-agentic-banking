import 'package:flutter/material.dart';

import '../access_text.dart';
import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../money.dart';
import 'mandates_screen.dart';

/// El centro de permisos y privacidad (F3-06): todo lo que tiene acceso a la
/// cuenta, en un solo sitio, y retirarlo con un toque.
///
/// Las plataformas que pueden pagar, los teléfonos vinculados, las passkeys, el
/// número en el directorio, y lo último que pasó. Lo que no se ve no se puede
/// revocar.
class AccessScreen extends StatefulWidget {
  const AccessScreen({super.key, required this.client, this.onUnlinkThisPhone, this.mandates});

  final BffClient client;

  /// Desvincular el teléfono con el que se está usando la app: borra también sus
  /// llaves y saca de la app. Nulo si este teléfono no está vinculado.
  final Future<void> Function()? onUnlinkThisPhone;

  /// Abre los permisos de pago con su gasto, para ver el detalle.
  final MandatesScreen Function()? mandates;

  @override
  State<AccessScreen> createState() => _AccessScreenState();
}

class _AccessScreenState extends State<AccessScreen> {
  AccessOverview? _access;
  ApiError? _error;
  bool _busy = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final access = await widget.client.access();
      if (mounted) {
        setState(() {
          _access = access;
          _error = null;
        });
      }
    } on ApiError catch (e) {
      if (mounted) setState(() => _error = e);
    }
  }

  /// Confirma, ejecuta y recarga. Un «no» del banco se explica, no se esconde.
  Future<void> _revoke({required String title, required String body, required String action, required Future<void> Function() run}) async {
    final sure = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(title),
        content: Text(body),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancelar')),
          FilledButton(onPressed: () => Navigator.pop(context, true), child: Text(action)),
        ],
      ),
    );
    if (sure != true) return;
    setState(() => _busy = true);
    try {
      await run();
      await _load();
    } on ApiError catch (e) {
      if (mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final access = _access;
    return Scaffold(
      appBar: AppBar(title: const Text('Permisos y privacidad')),
      body: switch ((access, _error)) {
        (_, final ApiError error?) => Center(child: Text(error.userMessage)),
        (null, _) => const Center(child: CircularProgressIndicator()),
        (final AccessOverview a, _) => RefreshIndicator(
            onRefresh: _load,
            child: ListView(
              padding: const EdgeInsets.fromLTRB(16, 8, 16, 32),
              children: [
                Text('Quién y qué tiene acceso a tu cuenta. Puedes retirarlo cuando quieras.',
                    style: Theme.of(context).textTheme.bodyMedium),
                _Section(
                  title: 'Pueden pagar desde tu cuenta',
                  empty: 'Ninguna plataforma tiene permiso de pago.',
                  footer: widget.mandates == null
                      ? null
                      : TextButton(
                          onPressed: () => Navigator.of(context)
                              .push(MaterialPageRoute<void>(builder: (_) => widget.mandates!()))
                              .then((_) => _load()),
                          child: const Text('Ver cuánto gastó cada una'),
                        ),
                  children: [
                    for (final m in a.platforms)
                      _Row(
                        icon: Icons.smart_toy_outlined,
                        title: m.holderName,
                        subtitle: [
                          if (m.maxPerOperationMicros != null)
                            'Hasta ${Money(m.maxPerOperationMicros!, m.currency).format()} por pago',
                          'hasta el ${shortDate(m.expiresAt).split(' ').first}',
                        ].join(' · '),
                        action: 'Retirar',
                        onAction: _busy
                            ? null
                            : () => _revoke(
                                  title: '¿Retirar el permiso a ${m.holderName}?',
                                  body: 'No podrá iniciar pagos nuevos desde tu cuenta. Los que ya estén en curso '
                                      'pueden completarse.',
                                  action: 'Retirar',
                                  run: () => widget.client.revokeMandate(m.id),
                                ),
                      ),
                  ],
                ),
                _Section(
                  title: 'Teléfonos vinculados',
                  empty: 'No tienes teléfonos vinculados.',
                  children: [
                    for (final p in a.phones)
                      _Row(
                        icon: Icons.smartphone,
                        title: p.name,
                        subtitle: p.current
                            ? 'Este teléfono'
                            : p.locked
                                ? 'Bloqueado por claves equivocadas'
                                : p.lastUsedAt == null
                                    ? 'Sin usar todavía'
                                    : 'Usado el ${shortDate(p.lastUsedAt!)}',
                        action: 'Desvincular',
                        onAction: _busy || (p.current && widget.onUnlinkThisPhone == null)
                            ? null
                            : () => _revoke(
                                  title: '¿Desvincular ${p.current ? 'este teléfono' : p.name}?',
                                  body: p.current
                                      ? 'Ya no podrás entrar desde aquí con tu cara ni con tu clave, y saldrás de la app.'
                                      : 'Ya no podrá entrar a tu cuenta ni confirmar operaciones.',
                                  action: 'Desvincular',
                                  run: p.current ? widget.onUnlinkThisPhone! : () => widget.client.revokeDevice(p.id),
                                ),
                      ),
                  ],
                ),
                _Section(
                  title: 'Passkeys',
                  empty: 'No tienes passkeys.',
                  children: [
                    for (final k in a.passkeys)
                      _Row(
                        icon: Icons.key_outlined,
                        title: k.synced ? 'Passkey en tu llavero' : 'Passkey de un dispositivo',
                        subtitle: [
                          if (k.current) 'Entraste con ella',
                          if (k.synced) 'Se sincroniza con iCloud o Google',
                          'creada el ${shortDate(k.createdAt).split(' ').first}',
                        ].join(' · '),
                        action: 'Quitar',
                        onAction: _busy
                            ? null
                            : () => _revoke(
                                  title: '¿Quitar esta passkey?',
                                  body: 'Seguirá en tu llavero, pero el banco ya no la aceptará para entrar.',
                                  action: 'Quitar',
                                  run: () => widget.client.removePasskey(k.id),
                                ),
                      ),
                  ],
                ),
                _Section(
                  title: 'Directorio de celulares',
                  empty: 'No estás en el directorio: nadie te encuentra por tu número.',
                  children: [
                    if (a.phoneDirectory case final phone?)
                      _Row(
                        icon: Icons.contact_phone_outlined,
                        title: phone,
                        subtitle: 'Otros te encuentran por este número para enviarte dinero',
                        action: 'Quitar',
                        onAction: _busy
                            ? null
                            : () => _revoke(
                                  title: '¿Quitar tu número del directorio?',
                                  body: 'Ya no podrán enviarte dinero escribiendo tu celular.',
                                  action: 'Quitar',
                                  run: widget.client.removePhoneAlias,
                                ),
                      ),
                  ],
                ),
                _Section(
                  title: 'Lo último que pasó',
                  empty: 'Todavía no hay actividad.',
                  children: [
                    for (final e in a.activity.take(15))
                      ListTile(
                        dense: true,
                        leading: Icon(
                          activityDenied(e) ? Icons.block : Icons.check_circle_outline,
                          color: activityDenied(e) ? Theme.of(context).colorScheme.error : null,
                        ),
                        title: Text(activityText(e)),
                        subtitle: Text(e.actorName),
                        trailing: Text(shortDate(e.at), style: Theme.of(context).textTheme.bodySmall),
                      ),
                  ],
                ),
              ],
            ),
          ),
      },
    );
  }
}

class _Section extends StatelessWidget {
  const _Section({required this.title, required this.empty, required this.children, this.footer});

  final String title;
  final String empty;
  final List<Widget> children;
  final Widget? footer;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 24),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.only(left: 4, bottom: 8),
            child: Text(title, style: theme.textTheme.titleSmall?.copyWith(fontWeight: FontWeight.w600)),
          ),
          Card(
            clipBehavior: Clip.antiAlias,
            child: children.isEmpty
                ? Padding(padding: const EdgeInsets.all(16), child: Text(empty, style: theme.textTheme.bodyMedium))
                : Column(
                    children: [
                      for (final (i, child) in children.indexed) ...[
                        if (i > 0) const Divider(height: 1, indent: 56),
                        child,
                      ],
                    ],
                  ),
          ),
          if (footer != null) Align(alignment: Alignment.centerLeft, child: footer),
        ],
      ),
    );
  }
}

class _Row extends StatelessWidget {
  const _Row({required this.icon, required this.title, required this.subtitle, required this.action, this.onAction});

  final IconData icon;
  final String title;
  final String subtitle;
  final String action;
  final VoidCallback? onAction;

  @override
  Widget build(BuildContext context) {
    return ListTile(
      leading: Icon(icon),
      title: Text(title),
      subtitle: Text(subtitle),
      trailing: TextButton(onPressed: onAction, child: Text(action)),
    );
  }
}

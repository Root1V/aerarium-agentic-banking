import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../history.dart';
import '../money.dart';
import '../theme.dart';
import 'credit_card_screen.dart';
import 'mandates_screen.dart';
import 'pending_payments_screen.dart';
import 'phone_alias_screen.dart';
import 'transfer_screen.dart';

/// Pantalla principal: saldo y últimos movimientos.
///
/// Se resuelve con UNA llamada al BFF. En redes móviles inestables, encadenar
/// tres peticiones para pintar una pantalla se nota mucho más que cualquier
/// optimización del servidor.
class HomeScreen extends StatefulWidget {
  const HomeScreen({
    super.key,
    required this.client,
    required this.accountId,
    this.onSignOut,
    this.onUnlink,
    this.confirmer,
    this.offerTestFunds = false,
  });

  final BffClient client;
  final String accountId;

  /// Vuelve a la entrada. Sin él —el sandbox, una sesión fija de desarrollo—
  /// no hay a dónde salir y la pantalla no ofrece hacerlo.
  final VoidCallback? onSignOut;

  /// Desvincula este teléfono de la cuenta. Sin él no se ofrece.
  final Future<void> Function()? onUnlink;

  /// Firma con el teléfono las operaciones de riesgo.
  final TransferConfirmer? confirmer;

  /// Mostrar el botón de saldo de prueba. Solo cuando el banco es un sandbox.
  final bool offerTestFunds;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  Home? _home;
  ApiError? _error;
  bool _loading = true;
  bool _funding = false;

  /// El destinatario de prueba que creó el sandbox junto con el saldo.
  Account? _testPayee;

  /// Si el banco ofrece tarjeta de crédito, y la del titular si ya la tiene.
  bool _creditOffered = false;
  CreditCard? _card;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    _loadCredit();
    try {
      final home = await widget.client.home(widget.accountId);
      if (!mounted) return;
      setState(() {
        _home = home;
        _loading = false;
      });
    } on ApiError catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('Mi cuenta'),
        actions: [
          // Los permisos de pago viven en la barra y no escondidos en un menú:
          // "¿a quién le di acceso a mi dinero?" es una pregunta que tiene que
          // poder responderse rápido.
          IconButton(
            icon: const Icon(Icons.key_outlined),
            tooltip: 'Permisos de pago',
            onPressed: _openMandates,
          ),
          if (widget.onSignOut != null)
            IconButton(
              icon: const Icon(Icons.logout),
              tooltip: 'Salir',
              onPressed: _signOut,
            ),
          PopupMenuButton<void>(
            tooltip: 'Más',
            itemBuilder: (_) => [
              PopupMenuItem(onTap: _openPhoneAlias, child: const Text('Mi número para recibir')),
              if (widget.onUnlink != null)
                PopupMenuItem(onTap: _confirmUnlink, child: const Text('Desvincular este teléfono')),
            ],
          ),
        ],
      ),
      body: RefreshIndicator(
        onRefresh: _load,
        child: _buildBody(),
      ),
      floatingActionButton: _home == null
          ? null
          : FloatingActionButton.extended(
              onPressed: _openTransfer,
              icon: const Icon(Icons.send),
              label: const Text('Enviar'),
            ),
    );
  }

  /// La tarjeta va aparte del inicio: si el banco no la ofrece o no responde, el
  /// inicio se muestra igual, sin ella.
  Future<void> _loadCredit() async {
    try {
      final card = await widget.client.creditCard();
      if (mounted) {
        setState(() {
          _creditOffered = true;
          _card = card;
        });
      }
    } catch (_) {
      // Cualquier fallo —el banco no ofrece tarjetas, no responde, o responde
      // algo ilegible— deja el inicio como estaba, sin la tarjeta. No se captura
      // solo ApiError a propósito: una respuesta mal formada no puede tumbar el
      // saldo, que es lo que la persona vino a ver.
      if (mounted) setState(() => _creditOffered = false);
    }
  }

  Future<void> _openCredit() async {
    final card = _card;
    if (card == null) {
      await Navigator.of(context).push<CreditCard>(MaterialPageRoute(
        builder: (_) => CreditApplyScreen(client: widget.client, accountId: widget.accountId),
      ));
    } else {
      await Navigator.of(context).push<void>(MaterialPageRoute(
        builder: (_) => CreditCardScreen(
          client: widget.client,
          accountId: widget.accountId,
          card: card,
          sandbox: widget.offerTestFunds,
        ),
      ));
    }
    // Pedirla o usarla cambia la tarjeta y el saldo.
    await _load();
  }

  Future<void> _addTestFunds() async {
    setState(() => _funding = true);
    try {
      final funds = await widget.client.addTestFunds(widget.accountId);
      _testPayee = funds.payee;
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(
        content: Text('Añadimos ${Money(funds.amountMicros, funds.currency).format()} de prueba. '
            'Puedes enviarle a ${funds.payee.name}'
            '${funds.payeePhone == null ? '' : ' al ${formatPhone(funds.payeePhone!)}'}.'),
      ));
      await _load();
    } on ApiError catch (e) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.userMessage)));
    } finally {
      if (mounted) setState(() => _funding = false);
    }
  }

  Future<void> _signOut() async {
    try {
      await widget.client.logout();
    } on ApiError {
      // Sin red no se puede avisar al banco, pero no se retiene a nadie dentro:
      // el token se descarta aquí y en el banco vence solo en 30 minutos.
    }
    widget.onSignOut!();
  }

  Future<void> _confirmUnlink() async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('¿Desvincular este teléfono?'),
        content: const Text(
          'Ya no podrás entrar desde aquí con tu cara ni con tu clave. Para volver a '
          'usarlo tendrás que entrar con tu passkey.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancelar')),
          TextButton(onPressed: () => Navigator.pop(context, true), child: const Text('Desvincular')),
        ],
      ),
    );
    if (confirmed == true) await widget.onUnlink!();
  }

  void _openPhoneAlias() {
    Navigator.of(context).push(MaterialPageRoute<void>(
      builder: (_) => PhoneAliasScreen(client: widget.client, accountId: widget.accountId),
    ));
  }

  void _openPending() {
    Navigator.of(context).push(MaterialPageRoute<void>(
      builder: (_) => PendingPaymentsScreen(
        client: widget.client,
        groups: _home!.pending,
      ),
    ));
  }

  void _openMandates() {
    Navigator.of(context).push(MaterialPageRoute<void>(
      builder: (_) => MandatesScreen(
        client: widget.client,
        testRequest: widget.offerTestFunds && _home != null
            ? TestConsentRequest(
                accountId: widget.accountId,
                currency: _home!.balance.currency,
                confirmer: widget.confirmer,
              )
            : null,
      ),
    ));
  }

  Widget _buildBody() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }

    if (_error != null) {
      final expired = _error!.code == ApiErrorCode.unauthenticated;
      if (expired && widget.onSignOut != null) {
        return _ErrorView(error: _error!, action: 'Volver a entrar', onAction: widget.onSignOut!);
      }
      return _ErrorView(error: _error!, action: 'Reintentar', onAction: _load);
    }

    final home = _home!;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        _BalanceCard(home: home, onOpenPending: _openPending),
        if (_creditOffered) ...[
          const SizedBox(height: 12),
          CreditCardTile(card: _card, onTap: _openCredit),
        ],
        if (widget.offerTestFunds) ...[
          const SizedBox(height: 8),
          Align(
            alignment: Alignment.centerLeft,
            child: TextButton.icon(
              onPressed: _funding ? null : _addTestFunds,
              icon: const Icon(Icons.science_outlined),
              label: const Text('Añadir saldo de prueba'),
            ),
          ),
        ],
        const SizedBox(height: 24),
        Text('Movimientos', style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 8),
        if (home.movements.isEmpty)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 32),
            child: Center(child: Text('Todavía no tienes movimientos')),
          )
        else
          ...groupHistory(home.movements, hasMore: home.hasMore).map((item) => switch (item) {
                SingleMovement(:final movement) => _MovementTile(movement: movement),
                PaymentGroup() => _PaymentGroupTile(group: item, onTap: _openMandates),
              }),
      ],
    );
  }

  Future<void> _openTransfer() async {
    final sent = await Navigator.of(context).push<bool>(
      MaterialPageRoute(
        builder: (_) => TransferScreen(
          client: widget.client,
          fromAccountId: widget.accountId,
          currency: _home!.balance.currency,
          confirmer: widget.confirmer,
          suggestedPayee: _testPayee,
        ),
      ),
    );
    // Tras un envío se recarga: mostrar un saldo viejo después de mover dinero
    // es la forma más rápida de perder la confianza de quien usa la app.
    if (sent == true) await _load();
  }
}

class _BalanceCard extends StatelessWidget {
  const _BalanceCard({required this.home, required this.onOpenPending});

  final Home home;
  final VoidCallback onOpenPending;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodyMedium?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      clipBehavior: Clip.antiAlias,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.all(20),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(home.account.name, style: muted),
                const SizedBox(height: AppSpacing.md),
                // "Disponible" y no "Saldo": lo retenido ya salió de aquí, y
                // llamar saldo a lo que queda haría pensar que falta dinero.
                Text('Disponible', style: theme.textTheme.labelLarge),
                const SizedBox(height: AppSpacing.xs),
                Text(
                  home.balance.format(),
                  key: const Key('balance'),
                  style: theme.textTheme.headlineMedium,
                ),
              ],
            ),
          ),
          // Solo si hay algo que contar. Ni "0,00 retenidos" —ruido que hace
          // dudar— ni una fila cuando el banco no pudo calcularlo: callar es
          // mejor que afirmar algo que no se sabe.
          if (home.hasRetained) ...[
            const Divider(height: 1),
            InkWell(
              key: const Key('retained'),
              onTap: onOpenPending,
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 20, vertical: 14),
                child: Row(
                  children: [
                    Icon(Icons.schedule, size: 20, color: theme.colorScheme.onSurfaceVariant),
                    const SizedBox(width: AppSpacing.md),
                    Expanded(
                      child: Text(
                        '${home.retained!.format()} retenidos en pagos en curso',
                        style: theme.textTheme.bodyMedium,
                      ),
                    ),
                    Icon(Icons.chevron_right, color: theme.colorScheme.onSurfaceVariant),
                  ],
                ),
              ),
            ),
          ],
        ],
      ),
    );
  }
}

/// Varios pagos seguidos a una misma plataforma, en una fila.
///
/// Lleva a los permisos y no a una lista de los pagos: lo que la persona quiere
/// hacer con cien micropagos de un agente es entender cuánto lleva y, si no le
/// cuadra, cortarlo — no leerlos uno por uno.
class _PaymentGroupTile extends StatelessWidget {
  const _PaymentGroupTile({required this.group, required this.onTap});

  final PaymentGroup group;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return ListTile(
      contentPadding: EdgeInsets.zero,
      onTap: onTap,
      leading: CircleAvatar(
        backgroundColor: theme.colorScheme.surfaceContainerHighest,
        child: Icon(Icons.smart_toy_outlined, color: theme.colorScheme.onSurfaceVariant),
      ),
      title: Text('Pagos a ${group.merchant}'),
      // "Últimos 10 pagos" y no "10 pagos" cuando la racha sigue más atrás de lo
      // cargado: el conteo de un grupo cortado no es el total.
      subtitle: Text(group.continuesBeyond
          ? 'Últimos ${group.count} pagos'
          : '${group.count} pagos'),
      trailing: Text(
        group.total.format(withSign: true),
        style: const TextStyle(
          fontWeight: FontWeight.w600,
          fontFeatures: [FontFeature.tabularFigures()],
        ),
      ),
    );
  }
}

class _MovementTile extends StatelessWidget {
  const _MovementTile({required this.movement});

  final Movement movement;

  @override
  Widget build(BuildContext context) {
    final incoming = movement.sign > 0;
    return ListTile(
      contentPadding: EdgeInsets.zero,
      leading: CircleAvatar(
        child: Icon(incoming ? Icons.south_west : Icons.north_east),
      ),
      title: Text(movement.label),
      // El concepto, salvo que repita el título ("Saldo de prueba" / "saldo de prueba").
      subtitle: movement.description.isEmpty ||
              movement.description.toLowerCase() == movement.label.toLowerCase()
          ? null
          : Text(movement.description),
      trailing: Text(
        movement.signedAmount.format(withSign: true),
        style: TextStyle(
          fontWeight: FontWeight.w600,
          fontFeatures: const [FontFeature.tabularFigures()],
          color: incoming ? context.moneyColors.positive : null,
        ),
      ),
    );
  }
}

class _ErrorView extends StatelessWidget {
  const _ErrorView({required this.error, required this.action, required this.onAction});

  final ApiError error;
  final String action;
  final VoidCallback onAction;

  @override
  Widget build(BuildContext context) {
    return ListView(
      children: [
        const SizedBox(height: 80),
        Center(
          child: Padding(
            padding: const EdgeInsets.all(24),
            child: Column(
              children: [
                Text(error.userMessage, textAlign: TextAlign.center),
                const SizedBox(height: 16),
                FilledButton(onPressed: onAction, child: Text(action)),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

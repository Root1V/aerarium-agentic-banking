import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import 'mandates_screen.dart';
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
  });

  final BffClient client;
  final String accountId;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> {
  Home? _home;
  ApiError? _error;
  bool _loading = true;

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

  void _openMandates() {
    Navigator.of(context).push(MaterialPageRoute<void>(
      builder: (_) => MandatesScreen(client: widget.client),
    ));
  }

  Widget _buildBody() {
    if (_loading) {
      return const Center(child: CircularProgressIndicator());
    }

    if (_error != null) {
      return _ErrorView(error: _error!, onRetry: _load);
    }

    final home = _home!;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        _BalanceCard(home: home),
        const SizedBox(height: 24),
        Text('Movimientos', style: Theme.of(context).textTheme.titleMedium),
        const SizedBox(height: 8),
        if (home.movements.isEmpty)
          const Padding(
            padding: EdgeInsets.symmetric(vertical: 32),
            child: Center(child: Text('Todavía no tienes movimientos')),
          )
        else
          ...home.movements.map((m) => _MovementTile(movement: m)),
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
        ),
      ),
    );
    // Tras un envío se recarga: mostrar un saldo viejo después de mover dinero
    // es la forma más rápida de perder la confianza de quien usa la app.
    if (sent == true) await _load();
  }
}

class _BalanceCard extends StatelessWidget {
  const _BalanceCard({required this.home});

  final Home home;

  @override
  Widget build(BuildContext context) {
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(20),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(home.account.name,
                style: Theme.of(context).textTheme.bodyMedium),
            const SizedBox(height: 8),
            Text(
              home.balance.format(),
              key: const Key('balance'),
              style: Theme.of(context).textTheme.headlineMedium,
            ),
          ],
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
      subtitle: movement.description.isEmpty ? null : Text(movement.description),
      trailing: Text(
        movement.signedAmount.format(withSign: true),
        style: TextStyle(
          fontWeight: FontWeight.w600,
          color: incoming ? Colors.green.shade700 : null,
        ),
      ),
    );
  }
}

class _ErrorView extends StatelessWidget {
  const _ErrorView({required this.error, required this.onRetry});

  final ApiError error;
  final VoidCallback onRetry;

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
                FilledButton(onPressed: onRetry, child: const Text('Reintentar')),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

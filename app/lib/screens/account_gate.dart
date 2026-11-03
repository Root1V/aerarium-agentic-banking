import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../auth/device_keys.dart';
import '../theme.dart';
import 'home_screen.dart';

/// El paso entre entrar y ver la cuenta.
///
/// La passkey dice quién es la persona, no qué cuenta quiere ver: se piden sus
/// cuentas y se abre la primera. Con varias (soles y dólares), el inicio deja
/// cambiar de una a otra, y aquí se recuerda cuál se está viendo.
class AccountGate extends StatefulWidget {
  const AccountGate({
    super.key,
    required this.client,
    required this.onSignOut,
    this.onUnlink,
    this.confirmer,
  });

  final BffClient client;
  final VoidCallback onSignOut;
  final Future<void> Function()? onUnlink;
  final TransferConfirmer? confirmer;

  @override
  State<AccountGate> createState() => _AccountGateState();
}

class _AccountGateState extends State<AccountGate> {
  late Future<List<Account>> _accounts = widget.client.listAccounts();

  /// La cuenta que se está viendo. Nula: la primera.
  String? _selected;

  /// Si el banco es un sandbox con saldo de prueba. Se pregunta en paralelo.
  late final Future<bool> _sandbox = widget.client.sandboxAvailable();
  bool _offerTestFunds = false;

  @override
  void initState() {
    super.initState();
    _sandbox.then((available) {
      if (mounted && available) setState(() => _offerTestFunds = true);
    });
  }

  @override
  Widget build(BuildContext context) {
    return FutureBuilder<List<Account>>(
      future: _accounts,
      builder: (context, snapshot) {
        if (snapshot.connectionState != ConnectionState.done) {
          return const Scaffold(body: Center(child: CircularProgressIndicator()));
        }
        final accounts = snapshot.data;
        if (accounts != null && accounts.isNotEmpty) {
          final current = accounts.firstWhere((a) => a.id == _selected, orElse: () => accounts.first);
          return HomeScreen(
            // Otra cuenta es otro inicio: saldo, movimientos y previsión propios.
            key: ValueKey(current.id),
            client: widget.client,
            accountId: current.id,
            accounts: accounts,
            onSwitchAccount: (id) => setState(() => _selected = id),
            onAccountsChanged: ({String? select}) => setState(() {
              _accounts = widget.client.listAccounts();
              if (select != null) _selected = select;
            }),
            onSignOut: widget.onSignOut,
            onUnlink: widget.onUnlink,
            confirmer: widget.confirmer,
            offerTestFunds: _offerTestFunds,
          );
        }

        final error = snapshot.error;
        final expired = error is ApiError && error.code == ApiErrorCode.unauthenticated;
        return Scaffold(
          body: Center(
            child: Padding(
              padding: const EdgeInsets.all(AppSpacing.xl),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Text(
                    switch (error) {
                      null => 'No tienes cuentas abiertas.',
                      ApiError() => error.userMessage,
                      _ => 'Algo salió mal. Inténtalo otra vez.',
                    },
                    textAlign: TextAlign.center,
                  ),
                  const SizedBox(height: AppSpacing.lg),
                  if (expired || error == null)
                    FilledButton(onPressed: widget.onSignOut, child: const Text('Volver a entrar'))
                  else
                    FilledButton(
                      onPressed: () => setState(() => _accounts = widget.client.listAccounts()),
                      child: const Text('Reintentar'),
                    ),
                ],
              ),
            ),
          ),
        );
      },
    );
  }
}

import 'package:flutter/material.dart';

import '../api/client.dart';
import '../api/errors.dart';
import '../api/models.dart';
import '../theme.dart';
import 'home_screen.dart';

/// El paso entre entrar y ver la cuenta.
///
/// La passkey dice quién es la persona, no qué cuenta quiere ver: se piden sus
/// cuentas y se abre la primera. Con varias cuentas, aquí irá el selector.
class AccountGate extends StatefulWidget {
  const AccountGate({super.key, required this.client, required this.onSignOut, this.onUnlink});

  final BffClient client;
  final VoidCallback onSignOut;
  final Future<void> Function()? onUnlink;

  @override
  State<AccountGate> createState() => _AccountGateState();
}

class _AccountGateState extends State<AccountGate> {
  late Future<List<Account>> _accounts = widget.client.listAccounts();

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
          return HomeScreen(
            client: widget.client,
            accountId: accounts.first.id,
            onSignOut: widget.onSignOut,
            onUnlink: widget.onUnlink,
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

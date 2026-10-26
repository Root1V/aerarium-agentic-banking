import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

import 'api/client.dart';
import 'api/models.dart';
import 'auth/device_keys.dart';
import 'auth/passkeys.dart';
import 'screens/account_gate.dart';
import 'screens/login_screen.dart';
import 'screens/onboarding_screen.dart';
import 'screens/protect_account_screen.dart';
import 'screens/unlock_screen.dart';

/// La puerta del banco: decide qué ve quien abre la app.
///
/// - Teléfono vinculado: Face ID directamente, sin pantalla de login.
/// - Teléfono sin vincular: entrar con passkey o abrir una cuenta. Las dos
///   terminan vinculando el teléfono, para que la próxima vez baste la cara.
///
/// El token de sesión vive solo en memoria: cerrar la app es cerrar la sesión, y
/// volver a entrar cuesta un segundo. Guardarlo en disco sería dejar una llave
/// del dinero de alguien en un archivo.
class BankEntry extends StatefulWidget {
  const BankEntry({
    super.key,
    required this.baseUrl,
    this.keys = const NativeDeviceKeys(),
    this.passkeys = const NativePasskeys(),
    this.httpClient,
    this.autoPrompt = true,
    this.notice,
  });

  final Uri baseUrl;
  final DeviceKeys keys;
  final PasskeyPlatform passkeys;

  /// Solo para pruebas.
  final http.Client? httpClient;

  /// Pedir Face ID al aparecer. No tras "Salir": quien acaba de salir no
  /// quiere que el teléfono le pida la cara al instante.
  final bool autoPrompt;

  /// Algo que decir al llegar, por ejemplo que el teléfono se desvinculó.
  final String? notice;

  @override
  State<BankEntry> createState() => _BankEntryState();
}

class _BankEntryState extends State<BankEntry> {
  late final Future<DeviceKeyStatus> _status = widget.keys.status();

  BffClient _client([String token = '']) =>
      BffClient(baseUrl: widget.baseUrl, token: () => token, httpClient: widget.httpClient);

  DeviceFlow _devices([String token = '']) => DeviceFlow(client: _client(token), keys: widget.keys);

  @override
  Widget build(BuildContext context) {
    return FutureBuilder<DeviceKeyStatus>(
      future: _status,
      builder: (context, snapshot) {
        final status = snapshot.data;
        if (status == null) {
          return const Scaffold(body: Center(child: CircularProgressIndicator()));
        }
        if (status.linked) {
          return UnlockScreen(
            flow: _devices(),
            deviceId: status.deviceId!,
            biometryName: status.biometryName,
            autoPrompt: widget.autoPrompt,
            onUnlocked: (session) => _enter(context, session),
            onUnlinked: (message) => _restart(context, notice: message),
          );
        }
        return LoginScreen(
          flow: PasskeyFlow(client: _client(), platform: widget.passkeys),
          notice: widget.notice,
          // Entró con su passkey en un teléfono sin vincular: se vincula ya,
          // con esa sesión, para no volver a pedirle la passkey.
          onSignedIn: (session) => _replace(context, (context) => ProtectAccountScreen(
                flow: _devices(session.token),
                biometryName: status.biometryName,
                onLinked: (linked) => _enter(context, linked),
              )),
          onOpenAccount: () => Navigator.of(context).push(MaterialPageRoute<void>(
            builder: (context) => OnboardingScreen(
              client: _client(),
              // El alta aprobada lleva directo a proteger la cuenta, sin pila
              // detrás: volver al formulario de un alta cerrada no tiene sentido.
              onApproved: (result) => _replace(context, (context) => ProtectAccountScreen(
                    flow: _devices(),
                    biometryName: status.biometryName,
                    onboardingReference: result.reference,
                    onLinked: (session) => _enter(context, session),
                  )),
            ),
          )),
        );
      },
    );
  }

  /// Cada pantalla navega con el contexto de su propia ruta: el de la anterior
  /// deja de existir al vaciarse la pila.
  void _replace(BuildContext context, WidgetBuilder builder) {
    Navigator.of(context).pushAndRemoveUntil(MaterialPageRoute<void>(builder: builder), (_) => false);
  }

  void _enter(BuildContext context, SessionGrant session) {
    _replace(context, (context) => AccountGate(
          client: _client(session.token),
          onSignOut: () => _restart(context, autoPrompt: false),
          onUnlink: () async {
            final status = await widget.keys.status();
            if (status.deviceId != null) await _devices(session.token).unlink(status.deviceId!);
            if (context.mounted) _restart(context, notice: 'Desvinculaste este teléfono.');
          },
        ));
  }

  void _restart(BuildContext context, {bool autoPrompt = true, String? notice}) {
    _replace(context, (_) => BankEntry(
          baseUrl: widget.baseUrl,
          keys: widget.keys,
          passkeys: widget.passkeys,
          httpClient: widget.httpClient,
          autoPrompt: autoPrompt,
          notice: notice,
        ));
  }
}

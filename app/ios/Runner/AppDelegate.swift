import AuthenticationServices
import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var passkeys: PasskeysChannel?

  override func application(
    _ application: UIApplication,
    didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
  ) -> Bool {
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    let registrar = engineBridge.pluginRegistry.registrar(forPlugin: "PasskeysChannel")!
    passkeys = PasskeysChannel(messenger: registrar.messenger())
  }
}

// MARK: - Passkeys

/// El autenticador de la plataforma, para la app: crea passkeys y firma desafíos
/// con el chip seguro del teléfono y Face ID o Touch ID.
///
/// Recibe y devuelve la serialización JSON de WebAuthn tal como la da y la espera
/// el banco. Aquí no se decide nada: el banco emite el desafío y verifica la
/// firma; este código solo traduce entre ese JSON y AuthenticationServices.
///
/// Para que el sistema acepte crear una passkey para el dominio del banco, la app
/// tiene que estar asociada a él: el entitlement `webcredentials:<dominio>` y el
/// archivo apple-app-site-association servido por HTTPS en ese dominio. Sin eso,
/// iOS rechaza la operación y la app lo muestra como "no disponible".
final class PasskeysChannel: NSObject {
  private let channel: FlutterMethodChannel
  /// Una ceremonia a la vez: el sistema muestra un solo diálogo.
  private var pending: Ceremony?

  init(messenger: FlutterBinaryMessenger) {
    channel = FlutterMethodChannel(name: "dev.aerarium/passkeys", binaryMessenger: messenger)
    super.init()
    channel.setMethodCallHandler { [weak self] call, result in
      self?.handle(call, result: result)
    }
  }

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    guard pending == nil else {
      result(FlutterError(code: "failed", message: "ya hay una ceremonia en curso", details: nil))
      return
    }
    guard
      let raw = call.arguments as? String,
      let decoded = try? JSONSerialization.jsonObject(with: Data(raw.utf8)) as? [String: Any]
    else {
      result(FlutterError(code: "failed", message: "opciones ilegibles", details: nil))
      return
    }
    // go-webauthn envuelve las opciones en `publicKey`, como navigator.credentials.
    let options = decoded["publicKey"] as? [String: Any] ?? decoded

    let request: ASAuthorizationRequest
    do {
      switch call.method {
      case "create": request = try registrationRequest(options)
      case "get": request = try assertionRequest(options)
      default:
        result(FlutterMethodNotImplemented)
        return
      }
    } catch {
      result(FlutterError(code: "failed", message: "\(error)", details: nil))
      return
    }

    let controller = ASAuthorizationController(authorizationRequests: [request])
    let ceremony = Ceremony(result: result) { [weak self] in self?.pending = nil }
    pending = ceremony
    controller.delegate = ceremony
    controller.presentationContextProvider = ceremony
    controller.performRequests()
  }

  private func registrationRequest(_ options: [String: Any]) throws -> ASAuthorizationRequest {
    guard
      let rp = options["rp"] as? [String: Any], let rpID = rp["id"] as? String,
      let user = options["user"] as? [String: Any],
      let userID = (user["id"] as? String).flatMap(Base64URL.decode),
      let name = user["name"] as? String,
      let challenge = (options["challenge"] as? String).flatMap(Base64URL.decode)
    else { throw PasskeyOptionsError.missing("rp, user o challenge") }

    let provider = ASAuthorizationPlatformPublicKeyCredentialProvider(relyingPartyIdentifier: rpID)
    let request = provider.createCredentialRegistrationRequest(
      challenge: challenge, name: name, userID: userID)
    // El banco exige verificación del usuario: la passkey cubre los dos factores
    // solo si quien la usa demostró ser el titular (biometría o código).
    request.userVerificationPreference = .required
    request.attestationPreference = .none
    return request
  }

  private func assertionRequest(_ options: [String: Any]) throws -> ASAuthorizationRequest {
    guard
      let rpID = options["rpId"] as? String,
      let challenge = (options["challenge"] as? String).flatMap(Base64URL.decode)
    else { throw PasskeyOptionsError.missing("rpId o challenge") }

    let provider = ASAuthorizationPlatformPublicKeyCredentialProvider(relyingPartyIdentifier: rpID)
    let request = provider.createCredentialAssertionRequest(challenge: challenge)
    request.userVerificationPreference = .required
    // Vacío en el login sin usuario: el sistema ofrece las passkeys del dominio.
    let allowed = options["allowCredentials"] as? [[String: Any]] ?? []
    request.allowedCredentials = allowed.compactMap { descriptor in
      (descriptor["id"] as? String).flatMap(Base64URL.decode)
        .map(ASAuthorizationPlatformPublicKeyCredentialDescriptor.init(credentialID:))
    }
    return request
  }
}

private enum PasskeyOptionsError: Error {
  case missing(String)
}

/// Una ceremonia en curso: recibe la respuesta del sistema y la devuelve a Dart.
private final class Ceremony: NSObject, ASAuthorizationControllerDelegate,
  ASAuthorizationControllerPresentationContextProviding
{
  private let result: FlutterResult
  private let done: () -> Void

  init(result: @escaping FlutterResult, done: @escaping () -> Void) {
    self.result = result
    self.done = done
  }

  func presentationAnchor(for controller: ASAuthorizationController) -> ASPresentationAnchor {
    let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
    return scenes.flatMap(\.windows).first(where: \.isKeyWindow) ?? ASPresentationAnchor()
  }

  func authorizationController(
    controller: ASAuthorizationController,
    didCompleteWithAuthorization authorization: ASAuthorization
  ) {
    switch authorization.credential {
    case let created as ASAuthorizationPlatformPublicKeyCredentialRegistration:
      guard let attestation = created.rawAttestationObject else {
        finish(FlutterError(code: "failed", message: "sin attestationObject", details: nil))
        return
      }
      finish(json(id: created.credentialID, response: [
        "clientDataJSON": Base64URL.encode(created.rawClientDataJSON),
        "attestationObject": Base64URL.encode(attestation),
        "transports": ["internal", "hybrid"],
      ]))
    case let signed as ASAuthorizationPlatformPublicKeyCredentialAssertion:
      finish(json(id: signed.credentialID, response: [
        "clientDataJSON": Base64URL.encode(signed.rawClientDataJSON),
        "authenticatorData": Base64URL.encode(signed.rawAuthenticatorData),
        "signature": Base64URL.encode(signed.signature),
        "userHandle": Base64URL.encode(signed.userID),
      ]))
    default:
      finish(FlutterError(code: "failed", message: "credencial inesperada", details: nil))
    }
  }

  func authorizationController(
    controller: ASAuthorizationController, didCompleteWithError error: Error
  ) {
    let code: String
    switch (error as? ASAuthorizationError)?.code {
    case .canceled: code = "cancelled"
    // Sin dominio asociado o sin un autenticador que pueda atender la petición.
    case .notHandled, .notInteractive: code = "unavailable"
    default: code = "failed"
    }
    finish(FlutterError(code: code, message: error.localizedDescription, details: nil))
  }

  private func json(id: Data, response: [String: Any]) -> Any {
    let credential: [String: Any] = [
      "id": Base64URL.encode(id),
      "rawId": Base64URL.encode(id),
      "type": "public-key",
      "authenticatorAttachment": "platform",
      "response": response,
      "clientExtensionResults": [String: Any](),
    ]
    guard let data = try? JSONSerialization.data(withJSONObject: credential) else {
      return FlutterError(code: "failed", message: "respuesta no serializable", details: nil)
    }
    return String(decoding: data, as: UTF8.self)
  }

  private func finish(_ value: Any) {
    result(value)
    done()
  }
}

private enum Base64URL {
  static func encode(_ data: Data) -> String {
    data.base64EncodedString()
      .replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_")
      .replacingOccurrences(of: "=", with: "")
  }

  static func decode(_ text: String) -> Data? {
    var base64 = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
    base64 += String(repeating: "=", count: (4 - base64.count % 4) % 4)
    return Data(base64Encoded: base64)
  }
}

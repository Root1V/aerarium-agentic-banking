import AuthenticationServices
import CryptoKit
import Flutter
import LocalAuthentication
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  private var passkeys: PasskeysChannel?
  private var deviceKeys: DeviceKeysChannel?

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
    deviceKeys = DeviceKeysChannel(messenger: registrar.messenger())
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

// MARK: - Llaves del teléfono

/// Las dos llaves que atan este teléfono a la cuenta, en el Secure Enclave.
///
/// - `biometric`: solo firma tras Face ID o Touch ID. Firmar con ella cubre los
///   dos factores: tener ESTE teléfono y ser quien es.
/// - `possession`: firma sin biometría. Acompaña a la clave de 6 dígitos, para
///   que el banco sepa que la clave se escribió en este teléfono.
///
/// Las privadas nunca salen del chip; el llavero solo guarda un envoltorio
/// cifrado que únicamente el chip de este teléfono puede abrir. No se copian a
/// iCloud: si el teléfono se pierde, las llaves se pierden con él.
///
/// En el simulador las llaves son de software y se dice (`hardwareBacked:
/// false`). Sirve para probar el flujo, no la seguridad.
final class DeviceKeysChannel: NSObject {
  private let channel: FlutterMethodChannel

  init(messenger: FlutterBinaryMessenger) {
    channel = FlutterMethodChannel(name: "dev.aerarium/device_keys", binaryMessenger: messenger)
    super.init()
    channel.setMethodCallHandler { [weak self] call, result in
      self?.handle(call, result: result)
    }
  }

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    let args = call.arguments as? [String: Any] ?? [:]
    switch call.method {
    case "status":
      result([
        "deviceId": Keychain.get(.deviceID).map { String(decoding: $0, as: UTF8.self) } as Any,
        "biometry": Self.biometryName(),
      ])
    case "create":
      do {
        result(try create())
      } catch let error as DeviceKeyError {
        result(error.flutter)
      } catch {
        result(FlutterError(code: "failed", message: "\(error)", details: nil))
      }
    case "sign":
      guard
        let which = args["key"] as? String,
        let payload = (args["payload"] as? String).flatMap(Base64URL.decode)
      else {
        result(FlutterError(code: "failed", message: "argumentos inválidos", details: nil))
        return
      }
      sign(which: which, payload: payload, reason: args["reason"] as? String ?? "Entrar a AIBank", result: result)
    case "save":
      guard let id = args["deviceId"] as? String else {
        result(FlutterError(code: "failed", message: "falta deviceId", details: nil))
        return
      }
      Keychain.set(Data(id.utf8), .deviceID)
      result(nil)
    case "reset":
      Keychain.Item.allCases.forEach(Keychain.delete)
      result(nil)
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  /// En el simulador `SecureEnclave.isAvailable` dice que sí, pero crear una
  /// llave atada a Face ID falla ("not supported on iOS Simulator"). Ahí se usan
  /// llaves de software; en un teléfono, siempre el chip.
  private static var useSecureEnclave: Bool {
    #if targetEnvironment(simulator)
      return false
    #else
      return SecureEnclave.isAvailable
    #endif
  }

  private static func biometryName() -> String {
    let context = LAContext()
    guard context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil) else {
      return "none"
    }
    switch context.biometryType {
    case .faceID: return "faceID"
    case .touchID: return "touchID"
    default: return "other"
    }
  }

  /// Crea las dos llaves, reemplazando las que hubiera, y devuelve las públicas.
  private func create() throws -> [String: Any] {
    // Sin biometría configurada no se puede crear una llave que la exija.
    guard Self.biometryName() != "none" else {
      throw DeviceKeyError.unavailable("sin Face ID ni Touch ID configurados")
    }
    Keychain.Item.allCases.forEach(Keychain.delete)

    if Self.useSecureEnclave {
      var error: Unmanaged<CFError>?
      // biometryCurrentSet: si alguien añade su cara o su huella al teléfono, la
      // llave deja de servir. Quien conozca el código del teléfono no puede
      // registrar su cara y entrar al banco con ella.
      guard
        let bioAccess = SecAccessControlCreateWithFlags(
          nil, kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly,
          [.privateKeyUsage, .biometryCurrentSet], &error),
        let possessionAccess = SecAccessControlCreateWithFlags(
          nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage], &error)
      else { throw DeviceKeyError.failed("control de acceso: \(String(describing: error))") }

      let bio = try SecureEnclave.P256.Signing.PrivateKey(accessControl: bioAccess)
      let possession = try SecureEnclave.P256.Signing.PrivateKey(accessControl: possessionAccess)
      Keychain.set(bio.dataRepresentation, .biometric)
      Keychain.set(possession.dataRepresentation, .possession)
      Keychain.set(Data([1]), .hardware)
      return [
        "biometricKey": Base64URL.encode(bio.publicKey.derRepresentation),
        "possessionKey": Base64URL.encode(possession.publicKey.derRepresentation),
        "hardwareBacked": true,
      ]
    }

    let bio = P256.Signing.PrivateKey()
    let possession = P256.Signing.PrivateKey()
    Keychain.set(bio.rawRepresentation, .biometric)
    Keychain.set(possession.rawRepresentation, .possession)
    Keychain.set(Data([0]), .hardware)
    return [
      "biometricKey": Base64URL.encode(bio.publicKey.derRepresentation),
      "possessionKey": Base64URL.encode(possession.publicKey.derRepresentation),
      "hardwareBacked": false,
    ]
  }

  private func sign(which: String, payload: Data, reason: String, result: @escaping FlutterResult) {
    let item: Keychain.Item = which == "biometric" ? .biometric : .possession
    guard let stored = Keychain.get(item) else {
      result(FlutterError(code: "unavailable", message: "este teléfono no tiene llaves", details: nil))
      return
    }
    let hardware = Keychain.get(.hardware) == Data([1])

    let finish: (Result<Data, DeviceKeyError>) -> Void = { outcome in
      DispatchQueue.main.async {
        switch outcome {
        case .success(let signature): result(Base64URL.encode(signature))
        case .failure(let error): result(error.flutter)
        }
      }
    }

    guard item == .biometric else {
      DispatchQueue.global(qos: .userInitiated).async {
        finish(Result { try Self.signature(stored, payload, hardware: hardware, context: nil) }
          .mapError { DeviceKeyError.failed("\($0)") })
      }
      return
    }

    // La biometría se pide primero con LocalAuthentication, que da errores claros
    // (canceló, quiso usar la clave, está bloqueada). El contexto ya evaluado se
    // le pasa al Secure Enclave, que firma sin volver a preguntar.
    let context = LAContext()
    context.localizedFallbackTitle = "Usar mi clave"
    context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, localizedReason: reason) {
      ok, error in
      guard ok else {
        finish(.failure(DeviceKeyError.from(error)))
        return
      }
      do {
        finish(.success(try Self.signature(stored, payload, hardware: hardware, context: context)))
      } catch {
        // La cara se reconoció pero la llave no firma: la biometría del teléfono
        // cambió desde que se creó (biometryCurrentSet) y la llave ya no sirve.
        finish(.failure(.invalidated("\(error)")))
      }
    }
  }

  private static func signature(_ stored: Data, _ payload: Data, hardware: Bool, context: LAContext?) throws -> Data {
    if hardware {
      let key = try SecureEnclave.P256.Signing.PrivateKey(
        dataRepresentation: stored, authenticationContext: context)
      return try key.signature(for: payload).derRepresentation
    }
    return try P256.Signing.PrivateKey(rawRepresentation: stored).signature(for: payload).derRepresentation
  }
}

private enum DeviceKeyError: Error {
  case cancelled, fallback, lockout
  case unavailable(String), invalidated(String), failed(String)

  static func from(_ error: Error?) -> DeviceKeyError {
    guard let la = error as? LAError else { return .failed("\(String(describing: error))") }
    switch la.code {
    case .userCancel, .appCancel, .systemCancel: return .cancelled
    case .userFallback: return .fallback
    case .biometryLockout: return .lockout
    case .biometryNotAvailable, .biometryNotEnrolled, .passcodeNotSet:
      return .unavailable(la.localizedDescription)
    default: return .failed(la.localizedDescription)
    }
  }

  var flutter: FlutterError {
    switch self {
    case .cancelled: return FlutterError(code: "cancelled", message: nil, details: nil)
    case .fallback: return FlutterError(code: "fallback", message: nil, details: nil)
    case .lockout: return FlutterError(code: "lockout", message: nil, details: nil)
    case .unavailable(let m): return FlutterError(code: "unavailable", message: m, details: nil)
    case .invalidated(let m): return FlutterError(code: "invalidated", message: m, details: nil)
    case .failed(let m): return FlutterError(code: "failed", message: m, details: nil)
    }
  }
}

/// El llavero, solo para este dispositivo y sin copia a iCloud.
private enum Keychain {
  enum Item: String, CaseIterable {
    case biometric, possession, hardware
    case deviceID = "device-id"
  }

  private static let service = "dev.aerarium.device-keys"

  private static func query(_ item: Item) -> [String: Any] {
    [
      kSecClass as String: kSecClassGenericPassword,
      kSecAttrService as String: service,
      kSecAttrAccount as String: item.rawValue,
    ]
  }

  static func set(_ data: Data, _ item: Item) {
    delete(item)
    var attributes = query(item)
    attributes[kSecValueData as String] = data
    attributes[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
    SecItemAdd(attributes as CFDictionary, nil)
  }

  static func get(_ item: Item) -> Data? {
    var q = query(item)
    q[kSecReturnData as String] = true
    q[kSecMatchLimit as String] = kSecMatchLimitOne
    var out: CFTypeRef?
    guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else { return nil }
    return out as? Data
  }

  static func delete(_ item: Item) {
    SecItemDelete(query(item) as CFDictionary)
  }
}

import Foundation

/// Session is what the backend hands back after a successful /login. The menu
/// bar persists this to ~/.doomsday/auth.json so the user only logs in once
/// per machine (until the session expires or they hit Sign Out).
struct Session: Codable, Equatable {
    let agentToken: String
    let sessionToken: String
    let sessionExpiresAt: Date
    let username: String

    var isExpired: Bool { sessionExpiresAt < Date() }
}

/// SessionStore reads/writes the local session blob. Plain JSON for now;
/// future versions should move to the macOS Keychain (kSecClassGenericPassword)
/// so the credentials aren't world-readable in the home directory.
enum SessionStore {
    static var path: String {
        NSHomeDirectory() + "/.doomsday/auth.json"
    }

    static func load() -> Session? {
        guard let data = try? Data(contentsOf: URL(fileURLWithPath: path)) else {
            return nil
        }
        let dec = JSONDecoder()
        dec.dateDecodingStrategy = .iso8601
        return try? dec.decode(Session.self, from: data)
    }

    static func save(_ session: Session) throws {
        let dir = (path as NSString).deletingLastPathComponent
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let enc = JSONEncoder()
        enc.dateEncodingStrategy = .iso8601
        enc.outputFormatting = .prettyPrinted
        let data = try enc.encode(session)
        let tmp = path + ".tmp"
        try data.write(to: URL(fileURLWithPath: tmp))
        _ = try? FileManager.default.removeItem(atPath: path)
        try FileManager.default.moveItem(atPath: tmp, toPath: path)
        // Tighten permissions: the file holds a long-lived agent token.
        try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
    }

    static func clear() {
        try? FileManager.default.removeItem(atPath: path)
    }
}

import Foundation

/// AuthClient POSTs username/password to the backend's /login endpoint.
/// Backend URL is read from $MOODIES_BACKEND_URL with a sensible localhost
/// default for development. In a release build the daemon binary has the URL
/// baked in via -ldflags; the menu bar reads the same value from a config
/// file the daemon writes (or, for v1, just uses the same env override).
enum AuthClient {

    enum LoginError: Error, LocalizedError {
        case badResponse(Int, String)
        case network(String)
        case invalidCredentials

        var errorDescription: String? {
            switch self {
            case .badResponse(let code, let body):
                return "Server returned HTTP \(code): \(body)"
            case .network(let msg):
                return "Network error: \(msg)"
            case .invalidCredentials:
                return "Invalid username or password."
            }
        }
    }

    /// Resolves the backend URL in priority order:
    ///   1. $MOODIES_BACKEND_URL env var (lets you point at a staging
    ///      backend without editing config files)
    ///   2. `backend_url = "..."` line in ~/.doomsday/config.toml (the
    ///      single source of truth the daemon also reads — written by the
    ///      installer or by `moodies config set`)
    ///   3. http://localhost:4000 dev default
    static var backendURL: String {
        if let v = ProcessInfo.processInfo.environment["MOODIES_BACKEND_URL"], !v.isEmpty {
            return v
        }
        if let v = readConfigBackendURL(), !v.isEmpty {
            return v
        }
        return "http://localhost:4000"
    }

    private static func readConfigBackendURL() -> String? {
        let path = NSHomeDirectory() + "/.doomsday/config.toml"
        guard let body = try? String(contentsOfFile: path, encoding: .utf8) else {
            return nil
        }
        for raw in body.split(separator: "\n", omittingEmptySubsequences: true) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            guard line.hasPrefix("backend_url") else { continue }
            // Quick TOML-ish parse — string after `=` with surrounding quotes
            // trimmed. Good enough for the one key we care about.
            guard let eq = line.firstIndex(of: "=") else { continue }
            let val = line[line.index(after: eq)...]
                .trimmingCharacters(in: .whitespaces)
                .trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
            return val
        }
        return nil
    }

    /// login POSTs to `/api/v1/agent/login` and decodes the response. The
    /// hostname is sent so the backend dashboard can label this agent.
    static func login(username: String, password: String) async throws -> Session {
        guard let url = URL(string: backendURL + "/api/v1/agent/login") else {
            throw LoginError.network("invalid backend URL")
        }

        let host = ProcessInfo.processInfo.hostName

        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.timeoutInterval = 10
        let body: [String: String] = [
            "username": username,
            "password": password,
            "hostname": host,
        ]
        req.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (data, resp): (Data, URLResponse)
        do {
            (data, resp) = try await URLSession.shared.data(for: req)
        } catch {
            throw LoginError.network(error.localizedDescription)
        }
        guard let http = resp as? HTTPURLResponse else {
            throw LoginError.network("non-HTTP response")
        }
        if http.statusCode == 401 {
            throw LoginError.invalidCredentials
        }
        if http.statusCode != 200 {
            let bodyStr = String(data: data, encoding: .utf8) ?? ""
            throw LoginError.badResponse(http.statusCode, bodyStr)
        }

        struct RawResponse: Decodable {
            let agent_token: String
            let session_token: String
            let session_expires_at: String
            let username: String
        }
        let raw = try JSONDecoder().decode(RawResponse.self, from: data)
        let iso = ISO8601DateFormatter()
        iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let expires = iso.date(from: raw.session_expires_at)
                    ?? ISO8601DateFormatter().date(from: raw.session_expires_at)
                    ?? Date().addingTimeInterval(86400)
        return Session(
            agentToken: raw.agent_token,
            sessionToken: raw.session_token,
            sessionExpiresAt: expires,
            username: raw.username
        )
    }
}

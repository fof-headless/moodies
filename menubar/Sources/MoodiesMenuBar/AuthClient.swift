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

    static var backendURL: String {
        if let v = ProcessInfo.processInfo.environment["MOODIES_BACKEND_URL"], !v.isEmpty {
            return v
        }
        // v1 ships dev defaults; matches the daemon's runtime default.
        return "http://localhost:4000"
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

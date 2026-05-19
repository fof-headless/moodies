import SwiftUI

/// LoginView is the modal SwiftUI form shown when no valid session is
/// cached. Two fields, one button. On success it persists the Session and
/// writes the agent_token where the daemon expects it (~/.doomsday/config.toml).
struct LoginView: View {
    @ObservedObject var session: SessionViewModel
    var onLoggedIn: () -> Void

    @State private var username = ""
    @State private var password = ""
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Sign in to Moodies")
                .font(.system(size: 16, weight: .semibold))
            Text("This connects the menu bar to your backend. v1 dev creds are user1 / user1.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)

            Form {
                TextField("Username", text: $username)
                    .textContentType(.username)
                    .disableAutocorrection(true)
                SecureField("Password", text: $password)
                    .textContentType(.password)
            }

            if let err = errorMessage {
                Text(err)
                    .font(.system(size: 11))
                    .foregroundStyle(.red)
            }

            HStack {
                Spacer()
                Button("Cancel") { NSApp.keyWindow?.close() }
                    .keyboardShortcut(.cancelAction)
                Button(isSubmitting ? "Signing in…" : "Sign In") {
                    Task { await submit() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(isSubmitting || username.isEmpty || password.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 360)
    }

    @MainActor
    private func submit() async {
        isSubmitting = true
        errorMessage = nil
        defer { isSubmitting = false }
        do {
            let sess = try await AuthClient.login(username: username, password: password)
            try SessionStore.save(sess)
            // Persist the agent_token where the daemon reads it. The daemon
            // also has a build-time override; the file write is for dev mode
            // where ldflags vars are empty.
            writeAgentTokenForDaemon(sess.agentToken)
            session.current = sess
            onLoggedIn()
        } catch let AuthClient.LoginError.invalidCredentials {
            errorMessage = "Invalid username or password."
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// writeAgentTokenForDaemon updates ~/.doomsday/config.toml in place so
    /// the daemon's next startup picks up the token. Minimal TOML rewriter —
    /// replaces or appends a single `agent_token = "..."` line.
    private func writeAgentTokenForDaemon(_ token: String) {
        let path = NSHomeDirectory() + "/.doomsday/config.toml"
        let line = "agent_token = \"\(token)\""
        var lines: [String] = []
        if let existing = try? String(contentsOfFile: path, encoding: .utf8) {
            for l in existing.split(separator: "\n", omittingEmptySubsequences: false) {
                if l.trimmingCharacters(in: .whitespaces).hasPrefix("agent_token") {
                    continue
                }
                lines.append(String(l))
            }
        }
        lines.append(line)
        let out = lines.joined(separator: "\n")
        let dir = (path as NSString).deletingLastPathComponent
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? out.write(toFile: path, atomically: true, encoding: .utf8)
    }
}

/// SessionViewModel is the shared observable holding the active Session (or
/// nil if the user is logged out). The MenuBarExtra branches on this so the
/// menu shows either the normal menu or a "Sign In…" entry.
@MainActor
final class SessionViewModel: ObservableObject {
    @Published var current: Session?

    init() {
        let cached = SessionStore.load()
        if let s = cached, !s.isExpired {
            self.current = s
        } else if cached != nil {
            // Expired — wipe so we don't accidentally try to reuse.
            SessionStore.clear()
        }
    }

    func signOut() {
        SessionStore.clear()
        current = nil
    }
}

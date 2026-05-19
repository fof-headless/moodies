import SwiftUI
import AppKit

@main
struct MoodiesMenuBarApp: App {
    @StateObject private var status = StatusModel()
    @StateObject private var session = SessionViewModel()

    var body: some Scene {
        MenuBarExtra {
            MenuContent(status: status, session: session, presentLogin: presentLogin)
        } label: {
            Image(systemName: iconFor(status.state, session: session))
        }
        .menuBarExtraStyle(.menu)

        // Standalone window scene used only for the Sign In sheet. We open
        // it via NSApp on first launch (or when the user picks Sign In) and
        // dismiss it from the LoginView once credentials are validated.
        Window("Sign in to Moodies", id: "login") {
            LoginView(session: session, onLoggedIn: dismissLogin)
        }
        .windowResizability(.contentSize)
        .defaultPosition(.center)
    }

    private func iconFor(_ s: AgentState, session: SessionViewModel) -> String {
        if session.current == nil {
            return "person.crop.circle.badge.questionmark"
        }
        switch s {
        case .capturing: return "dot.radiowaves.left.and.right"
        case .paused:    return "pause.circle"
        case .off:       return "circle.dotted"
        case .degraded:  return "exclamationmark.triangle"
        }
    }

    private func presentLogin() {
        // SwiftUI's Window scene is the cleanest cross-version way to bring
        // up an auxiliary modal from a MenuBarExtra-only app.
        if let url = URL(string: "moodies-menubar://login") {
            NSWorkspace.shared.open(url)
        }
        // Fallback for systems where the URL scheme isn't registered.
        if let win = NSApp.windows.first(where: { $0.identifier?.rawValue == "login" }) {
            win.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
        } else {
            // Use the openWindow environment via NSApp's app delegate. As a
            // last resort, surface the same intent via a notification.
            NSApp.sendAction(Selector(("showLoginWindow:")), to: nil, from: nil)
        }
    }

    private func dismissLogin() {
        if let win = NSApp.windows.first(where: { $0.identifier?.rawValue == "login" }) {
            win.close()
        }
    }
}

struct MenuContent: View {
    @ObservedObject var status: StatusModel
    @ObservedObject var session: SessionViewModel
    let presentLogin: () -> Void

    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Group {
            if let sess = session.current {
                Text("\(sess.username) — \(headerLine)")
                    .font(.system(size: 13, weight: .semibold))
            } else {
                Text("Moodies — not signed in")
                    .font(.system(size: 13, weight: .semibold))
            }
            if let detail = headerDetail {
                Text(detail).font(.system(size: 11)).foregroundStyle(.secondary)
            }

            Divider()

            if session.current == nil {
                signedOutSection
            } else {
                signedInSection
            }

            Divider()

            Button("Quit Menu Bar App") { NSApp.terminate(nil) }
                .keyboardShortcut("q")
        }
    }

    @ViewBuilder private var signedOutSection: some View {
        Button("Sign In…") {
            openWindow(id: "login")
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    @ViewBuilder private var signedInSection: some View {
        Button("Open Dashboard") { DaemonActions.openDashboard() }

        Divider()

        switch status.state {
        case .capturing, .degraded:
            Button("Pause Capture") {
                DaemonActions.pause()
                Task { await status.refresh() }
            }
        case .paused, .off:
            Button("Resume Capture") {
                DaemonActions.resume()
                Task { await status.refresh() }
            }
        }

        Divider()

        Button("Sign Out") { session.signOut() }
        Button("Uninstall Moodies…") {
            DaemonActions.uninstall()
            Task { await status.refresh() }
        }
    }

    private var headerLine: String {
        switch status.state {
        case .capturing: return "Capturing"
        case .paused:    return "Paused"
        case .off:       return "Off"
        case .degraded:  return "Degraded"
        }
    }

    private var headerDetail: String? {
        var parts: [String] = []
        if let age = status.lastHeartbeatAgo {
            let s = Int(age)
            parts.append("last beat \(s)s ago")
        }
        if status.eventCount > 0 {
            parts.append("\(status.eventCount) events tapped")
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

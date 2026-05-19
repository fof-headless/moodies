import SwiftUI
import AppKit

@main
struct MoodiesMenuBarApp: App {
    @StateObject private var status = StatusModel()

    var body: some Scene {
        MenuBarExtra {
            MenuContent(status: status)
        } label: {
            // SF Symbol shape changes per state. Color won't render because
            // MenuBarExtra forces template rendering; we lean on different
            // shapes (filled vs hollow, triangle vs circle) for at-a-glance
            // distinction in both light and dark menu bars.
            Image(systemName: iconFor(status.state))
        }
        .menuBarExtraStyle(.menu)
    }

    private func iconFor(_ s: AgentState) -> String {
        switch s {
        case .capturing: return "dot.radiowaves.left.and.right"
        case .paused:    return "pause.circle"
        case .off:       return "circle.dotted"
        case .degraded:  return "exclamationmark.triangle"
        }
    }
}

struct MenuContent: View {
    @ObservedObject var status: StatusModel

    var body: some View {
        // Status header (informational, not selectable).
        Text(headerLine)
            .font(.system(size: 13, weight: .semibold))
        if let detail = headerDetail {
            Text(detail).font(.system(size: 11)).foregroundStyle(.secondary)
        }

        Divider()

        Button("Open Dashboard") {
            DaemonActions.openDashboard()
        }

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

        Button("Uninstall Moodies…") {
            DaemonActions.uninstall()
            Task { await status.refresh() }
        }

        Divider()

        Button("Quit Menu Bar App") {
            NSApp.terminate(nil)
        }.keyboardShortcut("q")
    }

    private var headerLine: String {
        switch status.state {
        case .capturing: return "Moodies — Capturing"
        case .paused:    return "Moodies — Paused"
        case .off:       return "Moodies — Off"
        case .degraded:  return "Moodies — Degraded"
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

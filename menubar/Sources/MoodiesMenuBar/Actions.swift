import Foundation
import AppKit

/// DaemonActions wraps every shell-out the menu bar needs. Centralised so the
/// UI code stays declarative and so the binary-resolution / error-surfacing
/// logic lives in one place.
enum DaemonActions {

    // MARK: - Binary resolution

    /// Locates the `doomsday` (a.k.a. moodies) CLI binary. Search order:
    ///   1. $MOODIES_BIN env override
    ///   2. /opt/homebrew/bin/moodies (brew install)
    ///   3. ~/.local/bin/moodies
    ///   4. Repo-relative dev path under ~/dayjob/moodies/doomsday
    ///   5. `which` lookup on PATH (covers anything else)
    /// Returns nil if nothing is found — actions that need it should surface
    /// a user-visible error instead of silently failing.
    static func locateCLI() -> String? {
        if let override = ProcessInfo.processInfo.environment["MOODIES_BIN"],
           FileManager.default.isExecutableFile(atPath: override) {
            return override
        }
        let candidates = [
            "/opt/homebrew/bin/moodies",
            NSHomeDirectory() + "/.local/bin/moodies",
            NSHomeDirectory() + "/dayjob/moodies/doomsday",
        ]
        for c in candidates where FileManager.default.isExecutableFile(atPath: c) {
            return c
        }
        // Fallback: ask the shell. We use /usr/bin/env so PATH lookup applies.
        let task = Process()
        task.launchPath = "/usr/bin/env"
        task.arguments = ["which", "moodies"]
        let pipe = Pipe()
        task.standardOutput = pipe
        do {
            try task.run()
            task.waitUntilExit()
            let out = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if !out.isEmpty, FileManager.default.isExecutableFile(atPath: out) {
                return out
            }
        } catch {}
        return nil
    }

    /// Returns the integer UID of the current user, formatted for launchctl's
    /// gui/<uid>/<label> domain syntax.
    static func uid() -> String { String(getuid()) }

    // MARK: - Lifecycle

    /// Pause flips the disable_marker on, kills the running daemon + mitmdump,
    /// and disables PAC on every service. Reversible via resume().
    static func pause() {
        let marker = NSHomeDirectory() + "/.doomsday/disable_marker"
        try? "paused via menubar at \(ISO8601DateFormatter().string(from: Date()))\n"
            .data(using: .utf8)?
            .write(to: URL(fileURLWithPath: marker))

        // Best-effort kill; ignore errors since the processes may not be there.
        _ = run("/usr/bin/pkill", ["-f", "doomsday-daemon"])
        _ = run("/usr/bin/pkill", ["-f", "mitmdump"])

        if let cli = locateCLI() {
            // Use the disable helper to flip PAC off on all stored services.
            let binDir = (cli as NSString).deletingLastPathComponent
            let disableBin = binDir + "/moodies-disable"
            if FileManager.default.isExecutableFile(atPath: disableBin) {
                _ = run(disableBin, [])
            } else {
                // Fallback: networksetup directly on common services.
                for svc in ["Wi-Fi", "USB 10/100 LAN", "Thunderbolt Bridge"] {
                    _ = run("/usr/sbin/networksetup", ["-setautoproxystate", svc, "off"])
                }
            }
        }
    }

    /// Resume removes disable_marker, ensures the launchd plist is loaded,
    /// and kickstarts the daemon. PAC will come back on automatically via the
    /// daemon's watchdog once mitmdump binds 8080.
    static func resume() {
        let marker = NSHomeDirectory() + "/.doomsday/disable_marker"
        try? FileManager.default.removeItem(atPath: marker)

        let plistPath = NSHomeDirectory() + "/Library/LaunchAgents/com.doomsday.agent.plist"
        if FileManager.default.fileExists(atPath: plistPath) {
            // launchctl load is idempotent-ish — it errors if already loaded,
            // but that's fine; kickstart still does its job afterwards.
            _ = run("/bin/launchctl", ["load", plistPath])
            _ = run("/bin/launchctl", ["kickstart", "-k", "gui/\(uid())/com.doomsday.agent"])
        }
    }

    // MARK: - Dashboard / uninstall

    static func openDashboard() {
        NSWorkspace.shared.open(URL(string: "http://localhost:4000/")!)
    }

    /// Confirmed uninstall — runs `moodies uninstall` and reports the outcome
    /// via a follow-up alert.
    static func uninstall() {
        let confirm = NSAlert()
        confirm.messageText = "Uninstall Moodies?"
        confirm.informativeText = """
        This will remove the mitmproxy CA from the keychain, disable PAC on \
        every network service, unload the launchd agent, delete the claude \
        shim, and strip the moodies block from your shell rc files.

        Captured events in ~/.doomsday/buffer.db are NOT deleted.
        """
        confirm.alertStyle = .warning
        confirm.addButton(withTitle: "Uninstall")
        confirm.addButton(withTitle: "Cancel")
        guard confirm.runModal() == .alertFirstButtonReturn else { return }

        guard let cli = locateCLI() else {
            showError("Couldn't find the moodies CLI on this machine. Set MOODIES_BIN to its full path and try again.")
            return
        }

        let (status, output) = run(cli, ["uninstall"])
        if status == 0 {
            let done = NSAlert()
            done.messageText = "Moodies uninstalled."
            done.informativeText = output
            done.runModal()
        } else {
            showError("Uninstall returned exit \(status):\n\n\(output)")
        }
    }

    // MARK: - Helpers

    @discardableResult
    static func run(_ path: String, _ args: [String]) -> (Int32, String) {
        let task = Process()
        task.launchPath = path
        task.arguments = args
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = pipe
        do {
            try task.run()
            task.waitUntilExit()
            let out = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
            return (task.terminationStatus, out)
        } catch {
            return (-1, "exec \(path): \(error.localizedDescription)")
        }
    }

    static func showError(_ msg: String) {
        let a = NSAlert()
        a.messageText = "Moodies"
        a.informativeText = msg
        a.alertStyle = .critical
        a.runModal()
    }
}

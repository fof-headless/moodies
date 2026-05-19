import Foundation
import Combine
import Network

/// AgentState is the four-way status the menu bar cares about. Computed by
/// poking the same files/sockets the Go `doctor` command does, but inline so
/// we don't fork a child process every poll tick.
enum AgentState: String {
    case capturing  // mitmdump listening + heartbeat fresh + disable_marker absent
    case paused     // disable_marker present
    case off        // mitmdump not listening, no disable_marker (clean off)
    case degraded   // launchd loaded but port not responding, or stale heartbeat
}

/// StatusModel polls daemon state every `pollInterval` seconds and republishes
/// changes on the main actor for the SwiftUI MenuBarExtra to observe.
@MainActor
final class StatusModel: ObservableObject {
    @Published var state: AgentState = .off
    @Published var eventCount: Int = 0
    @Published var lastHeartbeatAgo: TimeInterval? = nil

    private var timer: Timer?
    private let pollInterval: TimeInterval = 5

    private var home: String { NSHomeDirectory() }
    private var doomsdayDir: String { home + "/.doomsday" }
    private var disableMarker: String { doomsdayDir + "/disable_marker" }
    private var heartbeatFile: String { doomsdayDir + "/heartbeat" }
    private var bufferDB: String { doomsdayDir + "/buffer.db" }

    init() {
        Task { await refresh() }
        timer = Timer.scheduledTimer(withTimeInterval: pollInterval, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in await self?.refresh() }
        }
    }

    deinit {
        timer?.invalidate()
    }

    func refresh() async {
        let disabled = FileManager.default.fileExists(atPath: disableMarker)
        let portUp = await tcpProbe(host: "127.0.0.1", port: 8080, timeout: 0.5)
        let hbAge = heartbeatAge()

        let newState: AgentState
        switch (disabled, portUp, hbAge) {
        case (true, _, _):
            newState = .paused
        case (false, true, let age?) where age < 120:
            newState = .capturing
        case (false, true, _):
            // Port is up but heartbeat is stale — daemon may be wedged.
            newState = .degraded
        case (false, false, _):
            newState = .off
        }

        if newState != state { state = newState }
        if hbAge != lastHeartbeatAgo { lastHeartbeatAgo = hbAge }

        // Cheap event count — count lines in raw_events.jsonl. Avoids loading
        // sqlite3 just for a number that's displayed best-effort.
        let path = doomsdayDir + "/raw_events.jsonl"
        if let data = try? String(contentsOfFile: path, encoding: .utf8) {
            let count = data.split(separator: "\n").count
            if count != eventCount { eventCount = count }
        }
    }

    /// Seconds since the daemon last touched ~/.doomsday/heartbeat, or nil if
    /// the file doesn't exist.
    private func heartbeatAge() -> TimeInterval? {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: heartbeatFile),
              let mod = attrs[.modificationDate] as? Date else {
            return nil
        }
        return Date().timeIntervalSince(mod)
    }

    /// Fast TCP probe — opens a connection, closes it. Resolves true if the
    /// kernel accepted the SYN within `timeout` seconds.
    private func tcpProbe(host: String, port: UInt16, timeout: TimeInterval) async -> Bool {
        await withCheckedContinuation { cont in
            let conn = NWConnection(
                host: NWEndpoint.Host(host),
                port: NWEndpoint.Port(integerLiteral: port),
                using: .tcp
            )
            var settled = false
            let lock = NSLock()
            let resolve: (Bool) -> Void = { ok in
                lock.lock(); defer { lock.unlock() }
                if settled { return }
                settled = true
                conn.cancel()
                cont.resume(returning: ok)
            }
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    resolve(true)
                case .failed, .cancelled:
                    resolve(false)
                default:
                    break
                }
            }
            conn.start(queue: .global())
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
                resolve(false)
            }
        }
    }
}

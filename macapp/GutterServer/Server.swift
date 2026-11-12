import AppKit
import Darwin

/// Runs the embedded gutter-server helper: folder bookmark, config, restarts, logs, status.
@MainActor
final class Server: ObservableObject {
    static let shared = Server()

    enum State: String { case stopped = "Stopped", starting = "Starting…", running = "Running", scanning = "Scanning" }

    @Published private(set) var state = State.stopped
    @Published private(set) var detail = ""
    @Published private(set) var folderPath: String?
    @Published private(set) var urls: [String] = [] // refreshed on each start

    var port: Int { let p = UserDefaults.standard.integer(forKey: "port"); return p > 0 ? p : 8081 }
    var hasFolder: Bool { UserDefaults.standard.data(forKey: Self.bookmarkKey) != nil }

    let support: URL
    var logURL: URL { support.appendingPathComponent("helper.log") }
    private static let bookmarkKey = "folderBookmark"

    private var proc: Process?
    private var wantRunning = false
    private var failures = 0
    private var startedAt = Date()
    private var folderURL: URL? // resolved, security scope held while the app runs
    private var poller: Timer?

    private init() {
        // Sandboxed: inside the app container. Developer ID: ~/Library/Application Support/Gutter.
        support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Gutter")
        try? FileManager.default.createDirectory(at: support, withIntermediateDirectories: true)
    }

    // MARK: lifecycle

    func boot() {
        if hasFolder { restart() } else { detail = "Choose a comics folder in Settings." }
    }

    func restart() {
        killHelper()
        wantRunning = true
        failures = 0
        launch()
    }

    func stop() {
        wantRunning = false
        killHelper()
        state = .stopped
        detail = ""
    }

    /// url carries a sandbox extension (open panel, or a folder opened with the app).
    func setFolder(_ url: URL) {
        do {
            let bm = try url.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil)
            UserDefaults.standard.set(bm, forKey: Self.bookmarkKey)
            folderURL?.stopAccessingSecurityScopedResource()
            folderURL = nil
            note("comics folder set to \(url.path)")
            restart()
        } catch {
            detail = "Could not save the folder: \(error.localizedDescription)"
        }
    }

    private func launch() {
        guard proc == nil, wantRunning else { return }
        guard let folder = resolveFolder() else { state = .stopped; return }
        if Keychain.password == nil { Keychain.password = Keychain.newPassword() }
        guard let exe = Bundle.main.url(forAuxiliaryExecutable: "gutter-server") else {
            detail = "Helper missing from the app bundle."; state = .stopped; return
        }
        do {
            // The helper can't use our app-scoped bookmark; a plain bookmark made
            // while we hold access carries the grant (see gutter-mac-spike SPIKE.md).
            let bmFile = support.appendingPathComponent("folder.bookmark")
            try folder.bookmarkData(options: [], includingResourceValuesForKeys: nil, relativeTo: nil).write(to: bmFile)
            // ponytail: rewritten on every start, so edits the server saves to it are lost; merge if that matters.
            let cfg = support.appendingPathComponent("config.yaml")
            try """
            port: \(port)
            db_path: "\(support.appendingPathComponent("gutter.db").path)"
            writeback_disabled: true
            """.write(to: cfg, atomically: true, encoding: .utf8)

            let p = Process()
            p.executableURL = exe
            p.arguments = ["-config", cfg.path, "-bookmark", bmFile.path]
            p.currentDirectoryURL = support
            var env = ProcessInfo.processInfo.environment
            env["GUTTER_PASSWORD"] = Keychain.password ?? ""
            p.environment = env
            let log = try openLog()
            p.standardOutput = log
            p.standardError = log
            p.terminationHandler = { pr in Task { @MainActor in Server.shared.exited(pr) } }
            try p.run()
            proc = p
            startedAt = Date()
            urls = Self.lanURLs(port: port)
            note("helper pid \(p.processIdentifier) on port \(port); addresses: \(urls.joined(separator: " "))")
            state = .starting
            detail = ""
            startPolling()
        } catch {
            detail = "Could not start: \(error.localizedDescription)"
            state = .stopped
        }
    }

    private func exited(_ p: Process) {
        guard p === proc else { return } // killHelper already handled it
        proc = nil
        note("helper exited with status \(p.terminationStatus)")
        state = .stopped
        guard wantRunning else { return }
        if Date().timeIntervalSince(startedAt) > 60 { failures = 0 }
        failures += 1
        let delay = min(pow(2, Double(failures)), 60)
        detail = "Server exited (\(p.terminationStatus)); restarting in \(Int(delay))s. See logs."
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak self] in self?.launch() }
    }

    /// SIGTERM, then SIGKILL after 3s. Synchronous so quit never leaves an orphan.
    private func killHelper() {
        poller?.invalidate()
        guard let p = proc else { return }
        proc = nil
        p.terminationHandler = nil
        p.terminate()
        let deadline = Date().addingTimeInterval(3)
        while p.isRunning && Date() < deadline { usleep(50_000) }
        if p.isRunning { kill(p.processIdentifier, SIGKILL) }
        note("helper stopped")
    }

    private func resolveFolder() -> URL? {
        if let u = folderURL { return u }
        guard let bm = UserDefaults.standard.data(forKey: Self.bookmarkKey) else {
            detail = "Choose a comics folder in Settings."; return nil
        }
        var stale = false
        guard let u = try? URL(resolvingBookmarkData: bm, options: .withSecurityScope, bookmarkDataIsStale: &stale) else {
            detail = "The comics folder can't be found. Choose it again in Settings."; return nil
        }
        _ = u.startAccessingSecurityScopedResource()
        if stale, let fresh = try? u.bookmarkData(options: .withSecurityScope, includingResourceValuesForKeys: nil, relativeTo: nil) {
            UserDefaults.standard.set(fresh, forKey: Self.bookmarkKey)
            note("refreshed stale bookmark for \(u.path)")
        }
        folderURL = u
        folderPath = u.path
        return u
    }

    // MARK: status

    private struct ScanStatus: Decodable { let is_scanning: Bool; let scanned: Int; let total: Int }

    private func startPolling() {
        poller?.invalidate()
        poller = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { _ in
            Task { @MainActor in await Server.shared.poll() }
        }
        Task { await poll() }
    }

    private func poll() async {
        guard proc != nil else { return }
        var req = URLRequest(url: URL(string: "http://127.0.0.1:\(port)/library/refresh/status")!, timeoutInterval: 2)
        req.setValue("Basic " + Data("gutter:\(Keychain.password ?? "")".utf8).base64EncodedString(), forHTTPHeaderField: "Authorization")
        guard let (data, resp) = try? await URLSession.shared.data(for: req),
              (resp as? HTTPURLResponse)?.statusCode == 200,
              let s = try? JSONDecoder().decode(ScanStatus.self, from: data), proc != nil else { return }
        state = s.is_scanning ? .scanning : .running
        detail = s.is_scanning && s.total > 0 ? "\(s.scanned) of \(s.total) files" : ""
    }

    /// Addresses phones and iPads on the LAN (or Tailscale) can use.
    private static func lanURLs(port: Int) -> [String] {
        var out: [String] = []
        var ifa: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifa) == 0 else { return [] }
        defer { freeifaddrs(ifa) }
        var cur = ifa
        while let i = cur?.pointee {
            defer { cur = i.ifa_next }
            let flags = Int32(i.ifa_flags)
            guard let a = i.ifa_addr, a.pointee.sa_family == UInt8(AF_INET),
                  flags & IFF_UP != 0, flags & IFF_LOOPBACK == 0 else { continue }
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            guard getnameinfo(a, socklen_t(a.pointee.sa_len), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 else { continue }
            let ip = String(cString: host)
            if !ip.hasPrefix("169.254."), !out.contains(ip) { out.append(ip) }
        }
        return out.map { "http://\($0):\(port)" }
    }

    // MARK: log

    /// helper.log gets the helper's stdout/stderr plus "[app]" lifecycle lines; rotated at 5 MB.
    private func openLog() throws -> FileHandle {
        let fm = FileManager.default
        if let size = try? fm.attributesOfItem(atPath: logURL.path)[.size] as? Int, size > 5_000_000 {
            let old = support.appendingPathComponent("helper.log.1")
            try? fm.removeItem(at: old)
            try? fm.moveItem(at: logURL, to: old)
        }
        // O_APPEND: the helper and the app both write here; plain offsets would clobber each other.
        let fd = open(logURL.path, O_WRONLY | O_APPEND | O_CREAT, 0o644)
        guard fd >= 0 else { throw CocoaError(.fileWriteUnknown) }
        return FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }

    private func note(_ s: String) {
        guard let h = try? openLog() else { return }
        h.write(Data("\(Date().ISO8601Format()) [app] \(s)\n".utf8))
        try? h.close()
    }
}

enum Keychain {
    private static let base: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "com.ryanhaines.gutter.server",
        kSecAttrAccount as String: "GUTTER_PASSWORD",
    ]

    static var password: String? {
        get {
            var q = base
            q[kSecReturnData as String] = true
            var out: AnyObject?
            guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
            return String(data: d, encoding: .utf8)
        }
        set {
            SecItemDelete(base as CFDictionary)
            guard let v = newValue, !v.isEmpty else { return }
            var q = base
            q[kSecValueData as String] = Data(v.utf8)
            SecItemAdd(q as CFDictionary, nil)
        }
    }

    static func newPassword() -> String {
        String(UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased().prefix(16))
    }
}

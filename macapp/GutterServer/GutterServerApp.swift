import ServiceManagement
import SwiftUI

@main
struct GutterServerApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @ObservedObject private var server = Server.shared

    var body: some Scene {
        MenuBarExtra("Gutter Server", systemImage: server.state == .stopped ? "books.vertical" : "books.vertical.fill") {
            MenuContent().environmentObject(server)
        }
        Settings {
            SettingsView().environmentObject(server)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        MainActor.assumeIsolated { Server.shared.boot() }
    }

    func application(_ application: NSApplication, open urls: [URL]) {
        guard let url = urls.first else { return }
        MainActor.assumeIsolated { Server.shared.setFolder(url) }
    }

    func applicationWillTerminate(_ notification: Notification) {
        MainActor.assumeIsolated { Server.shared.stop() }
    }
}

struct MenuContent: View {
    @EnvironmentObject var server: Server
    @Environment(\.openSettings) private var openSettings

    var body: some View {
        Text("Gutter Server: \(server.state.rawValue)")
        if !server.detail.isEmpty { Text(server.detail) }
        if server.state != .stopped {
            Section("Copy server address") {
                ForEach(server.urls, id: \.self) { url in
                    Button(url) {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(url, forType: .string)
                    }
                }
            }
        }
        Divider()
        if server.state == .stopped {
            Button("Start") { server.restart() }.disabled(!server.hasFolder)
        } else {
            Button("Stop") { server.stop() }
        }
        Button("Open Status Page") {
            NSWorkspace.shared.open(URL(string: "http://127.0.0.1:\(server.port)/")!)
        }.disabled(server.state == .stopped)
        Button("Show Logs") { NSWorkspace.shared.open(server.logURL) }
        Button("Settings…") {
            NSApp.activate(ignoringOtherApps: true) // LSUIElement apps open windows behind others otherwise
            openSettings()
        }.keyboardShortcut(",")
        Divider()
        Button("Quit Gutter Server") { NSApp.terminate(nil) }.keyboardShortcut("q")
    }
}

struct SettingsView: View {
    @EnvironmentObject var server: Server
    @AppStorage("port") private var port = 8081
    @State private var password = Keychain.password ?? ""
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled
    @State private var error = ""

    var body: some View {
        Form {
            LabeledContent("Comics folder") {
                HStack {
                    Text(server.folderPath ?? "None").lineLimit(1).truncationMode(.middle)
                    Button("Choose…", action: chooseFolder)
                }
            }
            TextField("Port", value: $port, format: .number.grouping(.never))
            TextField("Password", text: $password)
            Text("Use any user name with this password when you add the server in Gutter.")
                .font(.caption).foregroundStyle(.secondary)
            Toggle("Launch at login", isOn: $launchAtLogin)
                .onChange(of: launchAtLogin) { _, on in
                    do { try on ? SMAppService.mainApp.register() : SMAppService.mainApp.unregister() }
                    catch { self.error = error.localizedDescription; launchAtLogin = SMAppService.mainApp.status == .enabled }
                }
            if !error.isEmpty { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("Save and Restart Server") {
                    guard (1...65535).contains(port), !password.isEmpty else { error = "Set a port (1–65535) and a password."; return }
                    error = ""
                    Keychain.password = password
                    if server.hasFolder { server.restart() }
                }.keyboardShortcut(.defaultAction)
            }
        }
        .formStyle(.grouped)
        .frame(width: 480)
    }

    private func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.prompt = "Use Folder"
        panel.message = "Choose the folder that holds your comics."
        if panel.runModal() == .OK, let url = panel.url { server.setFolder(url) }
    }
}

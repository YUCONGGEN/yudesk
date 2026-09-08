import AppKit
import Foundation
import Darwin

// CFBundleExecutable must be this AppKit event-loop process. Finder's reopen
// AppleEvent is not delivered by starting an arbitrary Go executable again.
// This launcher has no windows, Dock icon, network listener, or UI credential.
final class Launcher: NSObject, NSApplicationDelegate {
    private var primary: Process?
    private var secondary: Process?
    private var stopping = false
    private var lastReopen = Date.distantPast
    private var coreURL: URL {
        URL(fileURLWithPath: CommandLine.arguments[0]).deletingLastPathComponent().appendingPathComponent("yudesk")
    }
    func applicationDidFinishLaunching(_ notification: Notification) {
        guard CommandLine.arguments.count == 1 else { NSApp.terminate(nil); return }
        let child = process()
        child.terminationHandler = { [weak self] _ in
            DispatchQueue.main.async {
                guard let self = self else { return }
                if let secondary = self.secondary, secondary.isRunning { secondary.terminate() }
                if self.stopping { NSApp.reply(toApplicationShouldTerminate: true) }
                else { NSApp.terminate(nil) }
            }
        }
        do { try child.run(); primary = child }
        catch { NSApp.terminate(nil) }
    }
    private func process() -> Process {
        let process = Process(); process.executableURL = coreURL
        process.arguments = []
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        return process
    }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        guard !stopping, primary?.isRunning == true, secondary?.isRunning != true,
              Date().timeIntervalSince(lastReopen) > 0.25 else { return false }
        lastReopen = Date()
        // YuDesk's existing single-instance protocol handles this short-lived
        // secondary launch and asks the existing core to show its current window.
        let child = process()
        child.terminationHandler = { [weak self] exited in
            DispatchQueue.main.async {
                if self?.secondary === exited { self?.secondary = nil }
            }
        }
        do {
            try child.run(); secondary = child
            // A wedged secondary must not turn repeated Finder opens into a
            // process leak. Only the child we started can be terminated here.
            DispatchQueue.main.asyncAfter(deadline: .now() + 10) { [weak self, weak child] in
                guard let child = child, self?.secondary === child, child.isRunning else { return }
                child.terminate()
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
                    if child.isRunning { Darwin.kill(child.processIdentifier, SIGKILL) }
                }
            }
        } catch { secondary = nil }
        return false
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let child = primary, child.isRunning else { return .terminateNow }
        if stopping { return .terminateLater }
        stopping = true
        if let secondary = secondary, secondary.isRunning { secondary.terminate() }
        child.terminate()
        DispatchQueue.main.asyncAfter(deadline: .now() + 3) {
            if child.isRunning { Darwin.kill(child.processIdentifier, SIGKILL) }
        }
        return .terminateLater
    }
}

let app = NSApplication.shared
let launcher = Launcher()
app.delegate = launcher
app.setActivationPolicy(.accessory)
app.run()

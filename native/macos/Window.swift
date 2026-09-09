import AppKit
import WebKit
import Foundation
import Darwin

// The pipe is private to the Go parent. Never include page URLs or JS error text
// in diagnostics: the local URL contains a bearer token.
private let maxLine = 16 * 1024
private func emit(_ event: String, _ code: String? = nil) {
    var value = ["event": event]
    if let code = code { value["code"] = code }
    if let data = try? JSONSerialization.data(withJSONObject: value) {
        let line = data + Data([10])
        _ = line.withUnsafeBytes { Darwin.write(STDOUT_FILENO, $0.baseAddress, $0.count) }
    }
}

struct LocalOrigin: Equatable {
    let port: Int
    static func parse(_ raw: String, requireToken: Bool) -> LocalOrigin? {
        guard raw.utf8.count <= maxLine, raw.hasPrefix("http://127.0.0.1:"),
              !raw.unicodeScalars.contains(where: { $0.value <= 32 || $0.value == 127 }),
              let c = URLComponents(string: raw), c.scheme == "http", c.host == "127.0.0.1",
              c.user == nil, c.password == nil, let port = c.port, (1...65535).contains(port),
              c.fragment == nil else { return nil }
        if requireToken {
            let values = (c.queryItems ?? []).filter { $0.name == "access_token" }
            guard values.count == 1, let token = values[0].value, !token.isEmpty,
                  !token.unicodeScalars.contains(where: { $0.value < 33 || $0.value == 127 }) else { return nil }
        }
        return LocalOrigin(port: port)
    }
    func permits(_ url: URL?) -> Bool {
        guard let url = url else { return false }
        // Fragment links are harmless navigation within the existing local page.
        guard var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return false }
        c.fragment = nil
        return c.string.flatMap { LocalOrigin.parse($0, requireToken: false) } == self
    }
}

private let dragScript = """
(() => {
  document.addEventListener('mousedown', e => {
    if (!e.isTrusted || e.button !== 0 || !(e.target instanceof Element)) return;
    if (!e.target.closest('[data-window-drag]') || e.target.closest('button,a,input,label,select,textarea,form,[contenteditable],[role="button"]')) return;
    window.webkit.messageHandlers.yudeskDrag.postMessage('drag');
  }, true);
})();
"""

final class ShellWindow: NSWindow {
    var lastPress: NSEvent?
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
    override func sendEvent(_ event: NSEvent) {
        if event.type == .leftMouseDown { lastPress = event }
        if event.type == .leftMouseUp { lastPress = nil }
        super.sendEvent(event)
    }
}

final class Shell: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate,
                   WKUIDelegate, WKScriptMessageHandler, WKDownloadDelegate {
    private var window: ShellWindow?
    private var web: WKWebView?
    private var origin: LocalOrigin?
    private var initialURL: URL?
    private var commandedClose = false
    private var loaded = false
    private var reloads = 0
    private var failureReported = false
    private var downloads: [WKDownload] = []
    private var cancelledDownloads = Set<ObjectIdentifier>()
    private let world = WKContentWorld.world(name: "YuDeskNativeWindow")

    func applicationDidFinishLaunching(_ notification: Notification) {
        let menu = NSMenu()
        let appItem = NSMenuItem(); menu.addItem(appItem)
        let appMenu = NSMenu(); appItem.submenu = appMenu
        appMenu.addItem(withTitle: "退出 YuDesk", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        let editItem = NSMenuItem(); menu.addItem(editItem)
        let editMenu = NSMenu(title: "编辑"); editItem.submenu = editMenu
        for (label, selector, key) in [("剪切", "cut:", "x"), ("复制", "copy:", "c"),
                                       ("粘贴", "paste:", "v"), ("全选", "selectAll:", "a")] {
            editMenu.addItem(withTitle: label, action: NSSelectorFromString(selector), keyEquivalent: key)
        }
        NSApp.mainMenu = menu
        DispatchQueue.global(qos: .userInitiated).async { self.readCommands() }
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        show(); return false
    }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if !commandedClose { commandedClose = true; emit("closed") }
        return .terminateNow
    }
    private func stop() {
        commandedClose = true
        NSApp.terminate(nil)
    }
    private func readCommands() {
        var pending = Data()
        var bytes = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = Darwin.read(STDIN_FILENO, &bytes, bytes.count)
            if n == 0 { DispatchQueue.main.async { self.stop() }; return }
            if n < 0 {
                if errno == EINTR { continue }
                DispatchQueue.main.async { emit("error", "ipc_read_failed"); self.stop() }; return
            }
            for byte in bytes.prefix(n) {
                if byte == 10 {
                    let line = pending; pending.removeAll(keepingCapacity: true)
                    // Synchronous dispatch bounds the work queue even if the parent floods stdin.
                    DispatchQueue.main.sync { self.command(line) }
                } else {
                    if pending.count >= maxLine {
                        DispatchQueue.main.async { emit("error", "ipc_line_too_long"); self.stop() }; return
                    }
                    pending.append(byte)
                }
            }
        }
    }
    private func command(_ data: Data) {
        guard let value = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let action = value["action"] as? String,
              Set(value.keys).isSubset(of: ["action", "url"]) else { emit("error", "invalid_command"); return }
        if action != "open" && value["url"] != nil { emit("error", "invalid_command"); return }
        switch action {
        case "open":
            guard window == nil else { emit("error", "already_open"); return }
            guard let raw = value["url"] as? String, let allowed = LocalOrigin.parse(raw, requireToken: true),
                  let url = URL(string: raw) else { emit("error", "invalid_url"); return }
            origin = allowed; initialURL = url; open(url)
        case "show": guard window != nil else { emit("error", "not_open"); return }; show()
        case "hide": guard let window = window else { emit("error", "not_open"); return }; window.orderOut(nil); NSApp.setActivationPolicy(.accessory)
        case "minimize": guard let window = window else { emit("error", "not_open"); return }; NSApp.setActivationPolicy(.regular); window.miniaturize(nil)
        case "close": stop()
        default: emit("error", "invalid_command")
        }
    }
    private func open(_ url: URL) {
        let controller = WKUserContentController()
        controller.add(self, contentWorld: world, name: "yudeskDrag")
        controller.addUserScript(WKUserScript(source: dragScript, injectionTime: .atDocumentStart,
                                              forMainFrameOnly: true, in: world))
        let config = WKWebViewConfiguration()
        config.userContentController = controller
        config.websiteDataStore = .nonPersistent()
        config.mediaTypesRequiringUserActionForPlayback = []
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        // Public opt-in, default NO on macOS. WebKit still enforces the user's
        // trusted activation; enabling the API does not request fullscreen.
        config.preferences.isElementFullscreenEnabled = true
        config.applicationNameForUserAgent = "YuDeskNative/2.0.0"
        let web = WKWebView(frame: .zero, configuration: config)
        web.navigationDelegate = self; web.uiDelegate = self
        web.allowsBackForwardNavigationGestures = false
        let win = ShellWindow(contentRect: NSRect(x: 0, y: 0, width: 860, height: 600),
                              styleMask: [.borderless, .miniaturizable], backing: .buffered, defer: false)
        win.title = "YuDesk"; win.delegate = self; win.isReleasedWhenClosed = false
        win.contentMinSize = NSSize(width: 860, height: 600)
        win.contentMaxSize = NSSize(width: 860, height: 600)
        win.hasShadow = true; win.backgroundColor = .windowBackgroundColor
        win.contentView = web; win.center()
        self.window = win; self.web = web
        // The helper is the foreground Dock application; use the enclosing .app icon.
        let icon = URL(fileURLWithPath: CommandLine.arguments[0]).deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Resources/YuDesk.icns")
        if let image = NSImage(contentsOf: icon) { NSApp.applicationIconImage = image }
        show(); web.load(URLRequest(url: url))
        DispatchQueue.main.asyncAfter(deadline: .now() + 20) { [weak self] in
            guard let self = self, !self.loaded else { return }; self.failure("load_timeout")
        }
    }
    private func show() {
        guard let window = window else { return }
        NSApp.setActivationPolicy(.regular)
        if window.isMiniaturized { window.deminiaturize(nil) }
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
    }
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        if !commandedClose { emit("closed"); commandedClose = true }
        NSApp.terminate(nil); return true
    }
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.name == "yudeskDrag", message.frameInfo.isMainFrame,
              message.body as? String == "drag", origin?.permits(message.frameInfo.request.url) == true,
              let win = window, let press = win.lastPress,
              ProcessInfo.processInfo.systemUptime - press.timestamp < 0.5,
              NSEvent.pressedMouseButtons & 1 != 0 else { return }
        win.lastPress = nil; win.performDrag(with: press)
    }
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard origin?.permits(action.request.url) == true, action.targetFrame != nil else {
            decisionHandler(.cancel); return
        }
        decisionHandler(action.shouldPerformDownload ? .download : .allow)
    }
    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        guard origin?.permits(response.response.url) == true else { decisionHandler(.cancel); return }
        if let http = response.response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
            failure("load_http_error"); decisionHandler(.cancel); return
        }
        decisionHandler(response.canShowMIMEType ? .allow : .download)
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard origin?.permits(webView.url) == true else { return }
        // macOS versions can ship different WebKit builds. Do not announce ready
        // for a shell that cannot render the application's required APIs.
        webView.evaluateJavaScript("typeof fetch==='function' && typeof createImageBitmap==='function' && typeof AudioContext==='function' && typeof AbortSignal.timeout==='function' && typeof HTMLDialogElement==='function' && typeof HTMLDialogElement.prototype.showModal==='function' && !!document.createElement('canvas').getContext('2d')") { [weak self] result, error in
            guard let self = self else { return }
            guard error == nil, result as? Bool == true else { self.failure("unsupported_webkit"); return }
            self.loaded = true; self.failureReported = false; emit("ready")
        }
    }
    private func failure(_ code: String) { if !failureReported { failureReported = true; emit("error", code) } }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        if (error as NSError).code != NSURLErrorCancelled { failure("load_failed") }
    }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        if (error as NSError).code != NSURLErrorCancelled { failure("load_failed") }
    }
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        emit("error", "web_process_terminated")
        guard reloads < 2, let url = webView.url ?? initialURL, origin?.permits(url) == true else { return }
        reloads += 1
        DispatchQueue.main.asyncAfter(deadline: .now() + Double(reloads)) { [weak self] in
            guard let self = self, !self.commandedClose else { return }; self.web?.load(URLRequest(url: url))
        }
    }
    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? { nil }
    // App dialogs use HTML <dialog>. Never fall back to unstyled JS browser alerts.
    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        emit("error", "unexpected_script_dialog"); completionHandler()
    }
    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        emit("error", "unexpected_script_dialog"); completionHandler(false)
    }
    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
        emit("error", "unexpected_script_dialog"); completionHandler(nil)
    }
    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        guard origin?.permits(frame.request.url) == true, let window = window else { completionHandler(nil); return }
        let panel = NSOpenPanel(); panel.allowsMultipleSelection = parameters.allowsMultipleSelection
        panel.canChooseDirectories = parameters.allowsDirectories; panel.canChooseFiles = true
        panel.beginSheetModal(for: window) { response in completionHandler(response == .OK ? panel.urls : nil) }
    }
    func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) { track(download) }
    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) { track(download) }
    private func track(_ download: WKDownload) { downloads.append(download); download.delegate = self }
    private func transferStatus(_ state: String) {
        guard let web = web, origin?.permits(web.url) == true else { return }
        let script: String
        switch state {
        case "complete": script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='文件已保存';e.hidden=false;}})()"
        case "cancelled": script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='已取消保存文件';e.hidden=false;}})()"
        case "saving": script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='正在保存文件…';e.hidden=false;}})()"
        default: script = "(()=>{const e=document.getElementById('transferStatus');if(e){e.textContent='保存失败，请重试';e.hidden=false;}})()"
        }
        web.evaluateJavaScript(script, completionHandler: nil)
    }
    func download(_ download: WKDownload, decideDestinationUsing response: URLResponse, suggestedFilename: String,
                  completionHandler: @escaping (URL?) -> Void) {
        guard origin?.permits(response.url) == true, let window = window else { completionHandler(nil); return }
        let panel = NSSavePanel(); panel.nameFieldStringValue = URL(fileURLWithPath: suggestedFilename).lastPathComponent
        panel.beginSheetModal(for: window) { [weak self] result in
            if result == .OK { self?.transferStatus("saving"); completionHandler(panel.url) }
            else {
                self?.cancelledDownloads.insert(ObjectIdentifier(download)); self?.transferStatus("cancelled")
                completionHandler(nil)
            }
        }
    }
    func download(_ download: WKDownload, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                  decisionHandler: @escaping (WKDownload.RedirectPolicy) -> Void) {
        decisionHandler(origin?.permits(request.url) == true ? .allow : .cancel)
    }
    func downloadDidFinish(_ download: WKDownload) {
        downloads.removeAll { $0 === download }; cancelledDownloads.remove(ObjectIdentifier(download)); transferStatus("complete")
    }
    func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
        downloads.removeAll { $0 === download }
        if cancelledDownloads.remove(ObjectIdentifier(download)) != nil { transferStatus("cancelled"); return }
        transferStatus("failed"); emit("error", "download_failed")
    }
}

func selfTest() -> Bool {
    let good = "http://127.0.0.1:8234/?access_token=test"
    guard let origin = LocalOrigin.parse(good, requireToken: true), origin.port == 8234 else { return false }
    let invalid = ["http://localhost:8234/?access_token=x", "https://127.0.0.1:8234/?access_token=x",
                   "http://127.0.0.1:0/?access_token=x", "http://127.0.0.1:65536/?access_token=x",
                   "http://127.0.0.1:8234/", "http://127.0.0.1:8234/?access_token=",
                   "http://127.0.0.1:8234/?access_token=x&access_token=y",
                   "http://127.0.0.1:8234/?access_token=%00", "http://127.0.0.1:8234/?access_token=x#secret",
                   "http://127.0.0.1:8234@evil.test/?access_token=x", "http://127.0.0.1:8234/\n?access_token=x"]
    guard invalid.allSatisfy({ LocalOrigin.parse($0, requireToken: true) == nil }) else { return false }
    return origin.permits(URL(string: "http://127.0.0.1:8234/api/state")) &&
           !origin.permits(URL(string: "http://127.0.0.1:8235/?access_token=test")) &&
           !origin.permits(URL(string: "file:///etc/passwd"))
}

signal(SIGPIPE, SIG_IGN)
if CommandLine.arguments.count > 1 {
    if CommandLine.arguments == [CommandLine.arguments[0], "--self-test"] {
        let ok = selfTest(); emit(ok ? "self_test_passed" : "error", ok ? nil : "self_test_failed"); exit(ok ? 0 : 1)
    }
    emit("error", "invalid_arguments"); exit(2)
}
let app = NSApplication.shared
let shell = Shell()
app.delegate = shell
app.setActivationPolicy(.accessory)
app.run()

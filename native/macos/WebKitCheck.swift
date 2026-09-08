// Diagnostics only: no NSWindow, no activation, no input injection. This source
// is NOT linked into yudesk-window or shipped in an installer.
import AppKit
import WebKit
import Foundation

func finish(_ value: [String: Any], _ code: Int32) -> Never {
    if let data = try? JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]) {
        FileHandle.standardOutput.write(data + Data([10]))
    }
    exit(code)
}
final class Check: NSObject, WKNavigationDelegate {
    let web: WKWebView
    init(_ url: URL) {
        let config = WKWebViewConfiguration(); config.websiteDataStore = .nonPersistent()
        config.mediaTypesRequiringUserActionForPlayback = []
        config.preferences.isElementFullscreenEnabled = true
        web = WKWebView(frame: NSRect(x: 0, y: 0, width: 860, height: 600), configuration: config)
        super.init(); web.navigationDelegate = self
        web.load(URLRequest(url: url))
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        let body = """
        const result = {fetch:typeof fetch==='function',bitmap:typeof createImageBitmap==='function',audio:typeof AudioContext==='function',timeout:typeof AbortSignal.timeout==='function',dialog:typeof HTMLDialogElement.prototype.showModal==='function',clipboard:!!navigator.clipboard,fullscreen:document.fullscreenEnabled===true&&typeof document.documentElement.requestFullscreen==='function'};
        try { await document.documentElement.requestFullscreen();result.fullscreenGestureRequired=false; }
        catch (_) { result.fullscreenGestureRequired=true; }
        const canvas=document.createElement('canvas');canvas.width=2;canvas.height=2;
        const ctx=canvas.getContext('2d',{alpha:false,desynchronized:true});result.canvas=!!ctx;
        ctx.fillStyle='#12ab34';ctx.fillRect(0,0,2,2);
        const blob=await new Promise(resolve=>canvas.toBlob(resolve,'image/png'));
        const bitmap=await createImageBitmap(blob);ctx.clearRect(0,0,2,2);ctx.drawImage(bitmap,0,0);bitmap.close();
        const p=ctx.getImageData(0,0,1,1).data;result.pixel=p[0]===18&&p[1]===171&&p[2]===52;
        const audio=new AudioContext({latencyHint:'interactive'});await audio.close();
        const dialog=document.createElement('dialog');document.body.append(dialog);dialog.showModal();result.modal=dialog.open;dialog.close();
        const response=await fetch('/check',{signal:AbortSignal.timeout(2000)});result.request=(await response.text())==='ok';
        return result;
        """
        webView.callAsyncJavaScript(body, arguments: [:], in: nil, in: .page) { result in
            switch result {
            case .success(let value):
                guard let values = value as? [String: Bool], !values.isEmpty, values.values.allSatisfy({ $0 }) else {
                    finish(["event": "check_failed", "code": "missing_feature"], 1)
                }
                finish(["event": "check_passed", "features": values], 0)
            case .failure: finish(["event": "check_failed", "code": "javascript_failed"], 1)
            }
        }
    }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        finish(["event": "check_failed", "code": "load_failed"], 1)
    }
}
guard CommandLine.arguments.count == 2, let url = URL(string: CommandLine.arguments[1]),
      url.scheme == "http", url.host == "127.0.0.1", url.user == nil else {
    finish(["event": "check_failed", "code": "invalid_fixture"], 2)
}
let app = NSApplication.shared
app.setActivationPolicy(.prohibited)
let check = Check(url)
DispatchQueue.main.asyncAfter(deadline: .now() + 15) { finish(["event": "check_failed", "code": "timeout"], 1) }
app.run()

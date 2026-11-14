import UIKit
import XCTest

/// The single "test" keeps an HTTP API alive for Mobile. XCUITest is the only
/// sanctioned way to inject touches and read the accessibility tree on iOS.
final class RunnerTests: XCTestCase {
    private let agent = Agent()

    override func setUp() {
        continueAfterFailure = true
    }

    // XCUI calls report problems as test issues. Keep the runner alive and
    // hand the message back to the HTTP request that caused it.
    override func record(_ issue: XCTIssue) {
        // The full description embeds a whole accessibility dump; the first
        // line carries the reason.
        let first = issue.compactDescription.split(separator: "\n", maxSplits: 1).first.map(String.init) ?? ""
        agent.issue = String(first.prefix(400))
    }

    func testRunner() throws {
        let port = UInt16(ProcessInfo.processInfo.environment["APTEVA_PORT"] ?? "") ?? 22087
        guard let token = ProcessInfo.processInfo.environment["APTEVA_TOKEN"], !token.isEmpty else {
            throw NSError(domain: "AptevaRunner", code: 1, userInfo: [NSLocalizedDescriptionKey: "Missing runner authentication token"])
        }
        let agent = self.agent
        let server = try HTTPServer(port: port, token: token) { request in
            var response = HTTPResponse.error(500, "internal", "no response")
            DispatchQueue.main.sync { response = agent.handle(request) }
            return response
        }
        server.start()
        print("APTEVA_RUNNER_READY port=\(port)")
        while !agent.stopped {
            _ = RunLoop.current.run(mode: .default, before: Date().addingTimeInterval(0.5))
        }
        server.stop()
    }
}

final class Agent {
    var stopped = false
    var issue: String?
    private var known: [String] = [] // bundle ids Mobile touched, most recent first
    private let springboardID = "com.apple.springboard"

    private func remember(_ id: String) {
        known.removeAll { $0 == id }
        known.insert(id, at: 0)
        if known.count > 24 { known.removeLast() }
    }

    /// XCTest has no public "frontmost app" query, so check the apps this
    /// runner launched or opened links into, then fall back to SpringBoard.
    private func foreground(_ requested: String?) -> (String, XCUIApplication) {
        if let id = requested, !id.isEmpty { return (id, XCUIApplication(bundleIdentifier: id)) }
        for id in known {
            let app = XCUIApplication(bundleIdentifier: id)
            if app.state == .runningForeground { return (id, app) }
        }
        return (springboardID, XCUIApplication(bundleIdentifier: springboardID))
    }

    func handle(_ r: HTTPRequest) -> HTTPResponse {
        issue = nil
        let body = r.json()
        let response: HTTPResponse
        switch (r.method, r.path) {
        case ("GET", "/status"):
            response = status()
        case ("GET", "/screenshot"):
            response = HTTPResponse(contentType: "image/png", body: XCUIScreen.main.screenshot().pngRepresentation)
        case ("GET", "/source"):
            response = source(r.query["bundle_id"])
        case ("POST", "/tap"):
            response = tap(body)
        case ("POST", "/swipe"):
            response = swipe(body)
        case ("POST", "/type"):
            guard let text = body["text"] as? String, !text.isEmpty else { return .error(400, "invalid_argument", "text is required") }
            foreground(body["bundle_id"] as? String).1.typeText(text)
            response = .json(["ok": true])
        case ("POST", "/key"):
            response = key(body)
        case ("POST", "/app"):
            response = app(body)
        case ("POST", "/power"):
            response = power(body)
        case ("POST", "/open"):
            response = open(body)
        case ("POST", "/tap_element"):
            response = tapElement(body)
        case ("POST", "/stop"):
            stopped = true
            response = .json(["ok": true])
        default:
            response = .error(404, "not_found", "unknown runner endpoint")
        }
        if let issue, response.status < 400 {
            return .error(422, "action_failed", issue)
        }
        return response
    }

    private func status() -> HTTPResponse {
        let (id, app) = foreground(nil)
        let size = XCUIApplication(bundleIdentifier: springboardID).frame.size
        return .json([
            "ok": true,
            "name": UIDevice.current.name,
            "os": UIDevice.current.systemVersion,
            "model": UIDevice.current.model,
            "width": Double(size.width),
            "height": Double(size.height),
            "scale": Double(UIScreen.main.scale),
            "foreground": id,
            "foreground_state": app.state.rawValue,
        ])
    }

    /// Converts body coordinates to points in the target app's frame.
    /// unit "ratio" (default) is 0...1 of the app frame; "point" is absolute.
    private func point(_ app: XCUIApplication, _ body: [String: Any], _ kx: String, _ ky: String) -> XCUICoordinate? {
        guard let x = (body[kx] as? NSNumber)?.doubleValue, let y = (body[ky] as? NSNumber)?.doubleValue, x.isFinite, y.isFinite else { return nil }
        let origin = app.coordinate(withNormalizedOffset: CGVector(dx: 0, dy: 0))
        if (body["unit"] as? String) == "point" {
            return origin.withOffset(CGVector(dx: x, dy: y))
        }
        guard (0...1).contains(x), (0...1).contains(y) else { return nil }
        let f = app.frame
        return origin.withOffset(CGVector(dx: x * f.width, dy: y * f.height))
    }

    private func tap(_ body: [String: Any]) -> HTTPResponse {
        let app = foreground(body["bundle_id"] as? String).1
        guard let at = point(app, body, "x", "y") else { return .error(400, "invalid_argument", "x and y are required") }
        let hold = (body["hold_ms"] as? NSNumber)?.doubleValue ?? 0
        if hold > 0 {
            at.press(forDuration: min(hold, 10_000) / 1000)
        } else {
            at.tap()
        }
        return .json(["ok": true])
    }

    private func swipe(_ body: [String: Any]) -> HTTPResponse {
        let app = foreground(body["bundle_id"] as? String).1
        guard let from = point(app, body, "x", "y"), let to = point(app, body, "to_x", "to_y") else {
            return .error(400, "invalid_argument", "x, y, to_x and to_y are required")
        }
        let ms = max(50, min((body["duration_ms"] as? NSNumber)?.doubleValue ?? 400, 10_000))
        let a = from.screenPoint, b = to.screenPoint
        let distance = max(1, hypot(b.x - a.x, b.y - a.y))
        from.press(forDuration: 0.05, thenDragTo: to, withVelocity: XCUIGestureVelocity(rawValue: distance / (ms / 1000)), thenHoldForDuration: 0)
        return .json(["ok": true])
    }

    private func key(_ body: [String: Any]) -> HTTPResponse {
        switch body["key"] as? String {
        case "home":
            XCUIDevice.shared.press(.home)
        case "enter":
            foreground(body["bundle_id"] as? String).1.typeText("\n")
        case "delete":
            foreground(body["bundle_id"] as? String).1.typeText(XCUIKeyboardKey.delete.rawValue)
        #if !targetEnvironment(simulator)
        case "volume_up":
            XCUIDevice.shared.press(.volumeUp)
        case "volume_down":
            XCUIDevice.shared.press(.volumeDown)
        #endif
        default:
            return .error(400, "invalid_argument", "key must be home, enter, delete, volume_up or volume_down")
        }
        return .json(["ok": true])
    }

    private func app(_ body: [String: Any]) -> HTTPResponse {
        guard let id = body["bundle_id"] as? String, !id.isEmpty else { return .error(400, "invalid_argument", "bundle_id is required") }
        let app = XCUIApplication(bundleIdentifier: id)
        switch body["action"] as? String {
        case "launch":
            remember(id)
            app.activate()
        case "relaunch":
            remember(id)
            app.launch()
        case "terminate":
            app.terminate()
        case "state":
            break
        default:
            return .error(400, "invalid_argument", "action must be launch, relaunch, terminate or state")
        }
        return .json(["ok": true, "bundle_id": id, "state": stateName(app.state)])
    }

    private func open(_ body: [String: Any]) -> HTTPResponse {
        guard let raw = body["url"] as? String, let url = URL(string: raw), url.scheme != nil else {
            return .error(400, "invalid_argument", "url is required")
        }
        if let id = body["bundle_id"] as? String, !id.isEmpty { remember(id) }
        XCUIDevice.shared.system.open(url)
        return .json(["ok": true])
    }

    /// iOS has no public global idle-timer API. Drive its real Auto-Lock
    /// preference instead; disabling the runner app's idle timer would not keep
    /// other apps awake. Never send synthetic touches to the app under test.
    private func power(_ body: [String: Any]) -> HTTPResponse {
        let action = body["action"] as? String ?? "status"
        guard action == "status" || action == "set" else {
            return .error(400, "invalid_argument", "power action must be status or set")
        }
        let enabled = body["enabled"] as? Bool
        guard action != "set" || enabled != nil else {
            return .error(400, "invalid_argument", "enabled must be true or false")
        }
        guard !ProcessInfo.processInfo.isLowPowerModeEnabled else {
            return .error(409, "device_unavailable", "Turn off Low Power Mode on the iPhone to read or change Auto-Lock")
        }

        let (previousID, previousApp) = foreground(nil)
        let settingsID = "com.apple.Preferences"
        let settings = XCUIApplication(bundleIdentifier: settingsID)
        // Reading also visits Settings. Restore the foreground app after every
        // request, including errors. The panel must not poll this endpoint.
        defer {
            if previousID == springboardID { XCUIDevice.shared.press(.home) }
            else if previousID != settingsID { previousApp.activate() }
        }
        remember(settingsID)
        settings.activate()

        func cell(_ identifier: String, _ label: String) -> XCUIElement {
            let identified = settings.cells.matching(identifier: identifier).firstMatch
            if identified.exists { return identified }
            return settings.cells.containing(.staticText, identifier: label).firstMatch
        }
        func autoLock() -> XCUIElement { cell("AUTOLOCK", "Auto-Lock") }
        // Settings may have been left on any subpage. Prefer stable system
        // accessibility identifiers; labels are a fallback for older iOS.
        for _ in 0..<8 {
            if autoLock().exists { break }
            let display = cell("DISPLAY", "Display & Brightness")
            if display.exists { display.tap(); continue }
            let back = settings.navigationBars.buttons.firstMatch
            guard back.exists, back.isEnabled else { break }
            back.tap()
        }
        let row = autoLock()
        guard row.waitForExistence(timeout: 3), row.isEnabled else {
            return .error(409, "device_unavailable", "Auto-Lock is unavailable. Unlock the iPhone and check Low Power Mode or device policy in Settings > Display & Brightness")
        }
        func currentValue(_ row: XCUIElement) -> String? {
            if let value = row.value as? String, !value.isEmpty { return value }
            return row.staticTexts.allElementsBoundByIndex.map(\.label).first { !$0.isEmpty && $0 != "Auto-Lock" }
        }
        guard let current = currentValue(row) else {
            return .error(422, "unsupported", "This iOS Settings layout does not expose the current Auto-Lock value")
        }
        row.tap()
        func choice(_ label: String) -> XCUIElement {
            let predicate = NSPredicate(format: "identifier ==[c] %@ OR label ==[c] %@", label, label)
            let exact = settings.cells.matching(predicate).firstMatch
            if exact.exists { return exact }
            return settings.staticTexts.matching(predicate).firstMatch
        }
        let never = choice("Never")
        guard never.waitForExistence(timeout: 3) else {
            return .error(422, "unsupported", "This iOS Settings language or layout is not supported for automatic Auto-Lock changes. Set Auto-Lock in Settings > Display & Brightness")
        }
        guard choice(current).exists else {
            return .error(422, "unsupported", "Could not match the current Auto-Lock value to an option; no preference was changed")
        }
        let defaults = UserDefaults.standard
        let restoreKey = "apteva.autoLock.previousTimeout"
        let wasEnabled = current.caseInsensitiveCompare("Never") == .orderedSame
        var actual = current
        if action == "set", let enabled, enabled != wasEnabled {
            let target = enabled ? "Never" : (defaults.string(forKey: restoreKey) ?? "30 seconds")
            let option = choice(target)
            guard option.exists, option.isEnabled else {
                return .error(409, "device_unavailable", "Auto-Lock option '\(target)' is unavailable; check the iPhone's language and device policy")
            }
            // Save before changing so runner/session restarts retain restoration
            // information. Repeated enable requests must not overwrite it with Never.
            if enabled { defaults.set(current, forKey: restoreKey) }
            option.tap()
            let back = settings.navigationBars.buttons.firstMatch
            guard back.exists else {
                return .error(422, "action_failed", "Could not verify Auto-Lock after changing it")
            }
            back.tap()
            let updated = autoLock()
            guard updated.waitForExistence(timeout: 3), let value = currentValue(updated), value.caseInsensitiveCompare(target) == .orderedSame else {
                return .error(409, "device_unavailable", "The iPhone did not accept the Auto-Lock setting; check Low Power Mode and device policy")
            }
            actual = value
            if !enabled { defaults.removeObject(forKey: restoreKey) }
        }
        return .json([
            "keep_awake": actual.caseInsensitiveCompare("Never") == .orderedSame,
            "scope": "always",
            "keep_awake_while_charging": false,
            "charging_sources": [String](),
        ])
    }

    private func tapElement(_ body: [String: Any]) -> HTTPResponse {
        guard let value = body["value"] as? String, !value.isEmpty else { return .error(400, "invalid_argument", "value is required") }
        let app = foreground(body["bundle_id"] as? String).1
        let predicate: NSPredicate
        switch body["by"] as? String {
        case "identifier": predicate = NSPredicate(format: "identifier == %@", value)
        case "label": predicate = NSPredicate(format: "label == %@", value)
        default: predicate = NSPredicate(format: "identifier == %@ OR label == %@", value, value)
        }
        let element = app.descendants(matching: .any).matching(predicate).firstMatch
        let timeout = min(max((body["timeout_ms"] as? NSNumber)?.doubleValue ?? 3000, 0), 30_000) / 1000
        guard element.waitForExistence(timeout: timeout) else { return .error(404, "no_such_element", "no element matched " + value) }
        element.tap()
        return .json(["ok": true])
    }

    private func source(_ requested: String?) -> HTTPResponse {
        let (id, app) = foreground(requested)
        do {
            let snapshot = try app.snapshot()
            let screen = XCUIApplication(bundleIdentifier: springboardID).frame
            var out = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
            write(snapshot, screen: screen, into: &out, depth: 0)
            return HTTPResponse(contentType: "application/xml", body: Data(out.utf8))
        } catch {
            return .error(500, "source_failed", "could not read the UI of \(id): \(error.localizedDescription)")
        }
    }

    private func write(_ s: XCUIElementSnapshot, screen: CGRect, into out: inout String, depth: Int) {
        guard depth < 64, out.utf8.count < 4_000_000 else { return }
        let type = typeName(s.elementType)
        let f = s.frame
        let finite = f.minX.isFinite && f.minY.isFinite && f.width.isFinite && f.height.isFinite
        let visible = finite && f.width > 0 && f.height > 0 && f.intersects(screen)
        var value = ""
        if let v = s.value { value = String(describing: v) }
        let name = s.identifier.isEmpty ? s.label : s.identifier
        out += "<\(type) type=\"\(type)\" name=\"\(escape(name))\" label=\"\(escape(s.label))\" value=\"\(escape(value))\" enabled=\"\(s.isEnabled)\" visible=\"\(visible)\""
        if finite {
            out += " x=\"\(Int(f.minX))\" y=\"\(Int(f.minY))\" width=\"\(Int(f.width))\" height=\"\(Int(f.height))\""
        }
        if s.children.isEmpty {
            out += "/>\n"
            return
        }
        out += ">\n"
        for child in s.children { write(child, screen: screen, into: &out, depth: depth + 1) }
        out += "</\(type)>\n"
    }
}

private func escape(_ s: String) -> String {
    var out = ""
    out.reserveCapacity(s.count)
    for scalar in s.unicodeScalars {
        switch scalar {
        case "&": out += "&amp;"
        case "<": out += "&lt;"
        case ">": out += "&gt;"
        case "\"": out += "&quot;"
        case "\n": out += "&#10;"
        default:
            // XML 1.0 forbids most control characters.
            if scalar.value < 0x20 && scalar != "\t" { continue }
            out.unicodeScalars.append(scalar)
        }
    }
    return out
}

private func stateName(_ s: XCUIApplication.State) -> String {
    switch s {
    case .notRunning: return "not_running"
    case .runningBackgroundSuspended: return "suspended"
    case .runningBackground: return "background"
    case .runningForeground: return "foreground"
    default: return "unknown"
    }
}

// Mirrors XCUIElement.ElementType's raw values so the source matches the
// XCUIElementType* names other XCUITest tooling emits.
private let typeNames = [
    "Any", "Other", "Application", "Group", "Window", "Sheet", "Drawer", "Alert", "Dialog", "Button",
    "RadioButton", "RadioGroup", "CheckBox", "DisclosureTriangle", "PopUpButton", "ComboBox", "MenuButton",
    "ToolbarButton", "Popover", "Keyboard", "Key", "NavigationBar", "TabBar", "TabGroup", "Toolbar", "StatusBar",
    "Table", "TableRow", "TableColumn", "Outline", "OutlineRow", "Browser", "CollectionView", "Slider",
    "PageIndicator", "ProgressIndicator", "ActivityIndicator", "SegmentedControl", "Picker", "PickerWheel",
    "Switch", "Toggle", "Link", "Image", "Icon", "SearchField", "ScrollView", "ScrollBar", "StaticText",
    "TextField", "SecureTextField", "DatePicker", "TextView", "Menu", "MenuItem", "MenuBar", "MenuBarItem", "Map",
    "WebView", "IncrementArrow", "DecrementArrow", "Timeline", "RatingIndicator", "ValueIndicator", "SplitGroup",
    "Splitter", "RelevanceIndicator", "ColorWell", "HelpTag", "Matte", "DockItem", "Ruler", "RulerMarker", "Grid",
    "LevelIndicator", "Cell", "LayoutArea", "LayoutItem", "Handle", "Stepper", "Tab", "TouchBar", "StatusItem",
]

private func typeName(_ t: XCUIElement.ElementType) -> String {
    let i = Int(t.rawValue)
    return "XCUIElementType" + (i >= 0 && i < typeNames.count ? typeNames[i] : "Other")
}

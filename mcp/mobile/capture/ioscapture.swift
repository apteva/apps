// Mobile's iOS screen capture helper. It exposes USB-connected iPhones and
// iPads as AVFoundation capture devices (the path QuickTime uses) and records
// one to a QuickTime movie with H.264 video and the device's audio.
//
//   ioscapture list
//   ioscapture record --udid <udid> --out <file.mov> [--capture-id <id>] [--no-audio]
//
// record prints READY once frames are being written and finishes the file on
// SIGINT or SIGTERM. Errors are printed as "ERROR: <message>" with exit 1.
import AVFoundation
import CoreMediaIO
import Foundation

setvbuf(stdout, nil, _IOLBF, 0)

func fail(_ message: String) -> Never {
    print("ERROR: \(message)")
    exit(1)
}

func enableScreenCaptureDevices() {
    var address = CMIOObjectPropertyAddress(
        mSelector: CMIOObjectPropertySelector(kCMIOHardwarePropertyAllowScreenCaptureDevices),
        mScope: CMIOObjectPropertyScope(kCMIOObjectPropertyScopeGlobal),
        mElement: CMIOObjectPropertyElement(kCMIOObjectPropertyElementMain))
    var allow: UInt32 = 1
    CMIOObjectSetPropertyData(CMIOObjectID(kCMIOObjectSystemObject), &address, 0, nil, UInt32(MemoryLayout<UInt32>.size), &allow)
}

func screenDevices() -> [AVCaptureDevice] {
    let types: [AVCaptureDevice.DeviceType]
    if #available(macOS 14.0, *) {
        types = [.external]
    } else {
        types = [.externalUnknown]
    }
    return AVCaptureDevice.DiscoverySession(deviceTypes: types, mediaType: .muxed, position: .unspecified).devices
}

func normalized(_ id: String) -> String {
    id.lowercased().replacingOccurrences(of: "-", with: "")
}

// Devices appear asynchronously after the property is set.
func waitForDevices(matching match: ((AVCaptureDevice) -> Bool)?, seconds: Double) -> [AVCaptureDevice] {
    let deadline = Date().addingTimeInterval(seconds)
    var devices = screenDevices()
    while Date() < deadline {
        if let match = match, devices.contains(where: match) { break }
        RunLoop.current.run(until: Date().addingTimeInterval(0.25))
        devices = screenDevices()
    }
    return devices
}

func ensureCameraAccess() {
    switch AVCaptureDevice.authorizationStatus(for: .video) {
    case .authorized:
        return
    case .notDetermined:
        let done = DispatchSemaphore(value: 0)
        var granted = false
        AVCaptureDevice.requestAccess(for: .video) { ok in granted = ok; done.signal() }
        while done.wait(timeout: .now()) == .timedOut {
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        if !granted { fail("camera access was not granted; allow Camera for the app running the Mobile runner in System Settings > Privacy & Security") }
    default:
        fail("camera access is denied; allow Camera for the app running the Mobile runner in System Settings > Privacy & Security")
    }
}

final class Recorder: NSObject, AVCaptureFileOutputRecordingDelegate {
    func fileOutput(_ output: AVCaptureFileOutput, didStartRecordingTo fileURL: URL, from connections: [AVCaptureConnection]) {
        print("READY")
    }

    func fileOutput(_ output: AVCaptureFileOutput, didFinishRecordingTo outputFileURL: URL, from connections: [AVCaptureConnection], error: Error?) {
        if let error = error as NSError? {
            let finished = (error.userInfo[AVErrorRecordingSuccessfullyFinishedKey] as? Bool) ?? false
            if !finished { fail(error.localizedDescription) }
        }
        print("DONE")
        exit(0)
    }
}

var arguments = Array(CommandLine.arguments.dropFirst())
guard let command = arguments.first else { fail("usage: ioscapture list | record --udid <udid> --out <file>") }
arguments.removeFirst()

func option(_ name: String) -> String? {
    guard let i = arguments.firstIndex(of: name), i + 1 < arguments.count else { return nil }
    return arguments[i + 1]
}

enableScreenCaptureDevices()

switch command {
case "list":
    let devices = waitForDevices(matching: nil, seconds: 3)
    let rows = devices.map { ["id": $0.uniqueID, "name": $0.localizedName, "model": $0.modelID] }
    let data = try! JSONSerialization.data(withJSONObject: rows)
    print(String(data: data, encoding: .utf8)!)
    exit(0)

case "record":
    guard let udid = option("--udid"), let out = option("--out") else { fail("record needs --udid and --out") }
    let captureID = option("--capture-id")
    let wanted = Set([normalized(udid)] + (captureID.map { [normalized($0)] } ?? []))
    ensureCameraAccess()
    let match: (AVCaptureDevice) -> Bool = { wanted.contains(normalized($0.uniqueID)) }
    let devices = waitForDevices(matching: match, seconds: 8)
    guard let device = devices.first(where: match) else {
        let seen = devices.map { "\($0.localizedName) (\($0.uniqueID))" }.joined(separator: ", ")
        fail("the device is not available for USB screen capture; connect it by cable, unlock it and trust this Mac. Visible capture devices: \(seen.isEmpty ? "none" : seen). Set capture_id in devices_json if the IDs differ.")
    }
    let session = AVCaptureSession()
    do {
        let input = try AVCaptureDeviceInput(device: device)
        guard session.canAddInput(input) else { fail("cannot capture from \(device.localizedName)") }
        session.addInput(input)
    } catch {
        fail("cannot open \(device.localizedName): \(error.localizedDescription)")
    }
    let output = AVCaptureMovieFileOutput()
    guard session.canAddOutput(output) else { fail("cannot write a movie from this device") }
    session.addOutput(output)
    if let video = output.connection(with: .video) {
        output.setOutputSettings([AVVideoCodecKey: AVVideoCodecType.h264], for: video)
    }
    if arguments.contains("--no-audio"), let audio = output.connection(with: .audio) {
        audio.isEnabled = false
    }
    let recorder = Recorder()
    session.startRunning()
    let url = URL(fileURLWithPath: out)
    try? FileManager.default.removeItem(at: url)
    output.startRecording(to: url, recordingDelegate: recorder)

    var sources: [DispatchSourceSignal] = []
    for sig in [SIGINT, SIGTERM] {
        signal(sig, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: sig, queue: .main)
        source.setEventHandler {
            if output.isRecording { output.stopRecording() } else { exit(0) }
        }
        source.resume()
        sources.append(source)
    }
    withExtendedLifetime((recorder, session, sources)) {
        RunLoop.main.run()
    }

default:
    fail("unknown command \(command)")
}

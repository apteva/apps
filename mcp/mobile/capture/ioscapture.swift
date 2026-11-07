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

// USB device clocks can discontinuously change, and MovieFileOutput turns
// those changes into overlapping QuickTime edit lists. Use one monotonic host
// timeline for video and PCM sample counts for continuous audio instead.
func hostSeconds() -> Double { ProcessInfo.processInfo.systemUptime }

final class Recorder: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate, AVCaptureAudioDataOutputSampleBufferDelegate {
    let queue = DispatchQueue(label: "apteva.ioscapture.samples")
    let ingress = DispatchQueue(label: "apteva.ioscapture.ingress")
    private let url: URL
    private let audio: Bool
    private let realtime: Bool
    private var writer: AVAssetWriter?
    private var videoInput: AVAssetWriterInput?
    private var audioInput: AVAssetWriterInput?
    private var adaptor: AVAssetWriterInputPixelBufferAdaptor?
    private var origin: Double?
    private let stopLock = NSLock()
    private var stopRequested = false
    private var lastVideoTime = CMTime.invalid
    private var nextAudioTime = CMTime.invalid
    private var lastImage: CVPixelBuffer?
    private var audioSamples = 0
    private var finishing = false
    private var announcedReady = false
    private var ticker: DispatchSourceTimer?
    var onFinished: (() -> Void)?

    init(url: URL, audio: Bool, realtime: Bool = true) {
        self.url = url; self.audio = audio; self.realtime = realtime
    }

    private func time(_ host: Double) -> CMTime {
        CMTime(seconds: max(0, host - (origin ?? host)), preferredTimescale: 60_000)
    }

    private func ready(_ input: AVAssetWriterInput) -> Bool {
        // Offline regression fixtures may write faster than the encoder.
        if !realtime {
            let deadline = hostSeconds() + 5
            while !input.isReadyForMoreMediaData && hostSeconds() < deadline && writer?.status == .writing {
                Thread.sleep(forTimeInterval: 0.001)
            }
        }
        return input.isReadyForMoreMediaData
    }

    private func begin(_ image: CVPixelBuffer, at host: Double) {
        do {
            let w = try AVAssetWriter(outputURL: url, fileType: .mov)
            let input = AVAssetWriterInput(mediaType: .video, outputSettings: [
                AVVideoCodecKey: AVVideoCodecType.h264,
                AVVideoWidthKey: CVPixelBufferGetWidth(image),
                AVVideoHeightKey: CVPixelBufferGetHeight(image),
                AVVideoCompressionPropertiesKey: [
                    AVVideoAverageBitRateKey: 12_000_000,
                    AVVideoAllowFrameReorderingKey: false,
                    AVVideoExpectedSourceFrameRateKey: 60,
                ],
            ])
            input.expectsMediaDataInRealTime = realtime
            guard w.canAdd(input) else { fail("cannot encode USB video") }
            w.add(input)
            videoInput = input
            adaptor = AVAssetWriterInputPixelBufferAdaptor(assetWriterInput: input, sourcePixelBufferAttributes: nil)
            if audio {
                let input = AVAssetWriterInput(mediaType: .audio, outputSettings: [
                    AVFormatIDKey: kAudioFormatMPEG4AAC,
                    AVSampleRateKey: 48_000,
                    AVNumberOfChannelsKey: 2,
                    AVEncoderBitRateKey: 256_000,
                ])
                input.expectsMediaDataInRealTime = realtime
                guard w.canAdd(input) else { fail("cannot encode USB audio") }
                w.add(input); audioInput = input
            }
            writer = w
            guard w.startWriting() else { fail(w.error?.localizedDescription ?? "cannot start USB movie") }
            w.startSession(atSourceTime: .zero)
            origin = host
        } catch { fail(error.localizedDescription) }
    }

    func captureOutput(_ output: AVCaptureOutput, didOutput sample: CMSampleBuffer, from connection: AVCaptureConnection) {
        // Timestamp receipt before queueing work: encoder startup must not
        // turn pre-roll buffers into media captured after READY.
        let received = hostSeconds(), video = output is AVCaptureVideoDataOutput
        queue.async {
            if video { self.writeVideo(sample, at: received) }
            else { self.writeAudio(sample, at: received) }
        }
    }

    private func isStopping() -> Bool {
        stopLock.lock(); defer { stopLock.unlock() }; return stopRequested
    }

    func requestStop(at host: Double) {
        stopLock.lock()
        if stopRequested { stopLock.unlock(); return }
        stopRequested = true; stopLock.unlock()
        queue.async { self.finish(at: host) }
    }

    func writeVideo(_ sample: CMSampleBuffer, at host: Double) {
        guard !finishing, !isStopping(), CMSampleBufferDataIsReady(sample), let image = CMSampleBufferGetImageBuffer(sample) else { return }
        if writer == nil { begin(image, at: host) }
        lastImage = image
        if realtime && announcedReady { return }
        appendFrame(image, at: host)
    }

    private func appendFrame(_ image: CVPixelBuffer, at host: Double) {
        guard !isStopping(), let input = videoInput, ready(input) else { return }
        if !announcedReady { origin = host }
        var pts = time(host)
        if lastVideoTime.isValid && CMTimeCompare(pts, lastVideoTime) <= 0 {
            pts = CMTimeAdd(lastVideoTime, CMTime(value: 1, timescale: 60_000))
        }
        guard adaptor?.append(image, withPresentationTime: pts) == true else {
            fail(writer?.error?.localizedDescription ?? "cannot append USB video")
        }
        lastVideoTime = pts
        if !announcedReady {
            announcedReady = true
            if realtime {
                // Hardware encoder initialization may block the first append.
                // The first frame is already at zero; READY defines time zero
                // for subsequent video, audio, markers and stop timing.
                origin = hostSeconds()
                // The USB source can stop emitting frames on static screens.
                // A host-clock cadence holds the latest image and keeps audio
                // and video interleaving, independent of device timestamps.
                let timer = DispatchSource.makeTimerSource(queue: queue)
                timer.schedule(deadline: .now() + .nanoseconds(16_666_667), repeating: .nanoseconds(16_666_667), leeway: .milliseconds(1))
                timer.setEventHandler { [weak self] in
                    guard let self, !self.finishing, !self.isStopping(), let image = self.lastImage else { return }
                    self.appendFrame(image, at: hostSeconds())
                }
                ticker = timer; timer.resume()
            }
            print("READY")
        }
    }

    func writeAudio(_ sample: CMSampleBuffer, at host: Double) {
        guard !finishing, !isStopping(), announcedReady, let origin, host >= origin, let input = audioInput,
              CMSampleBufferDataIsReady(sample),
              let format = CMSampleBufferGetFormatDescription(sample),
              let description = CMAudioFormatDescriptionGetStreamBasicDescription(format) else { return }
        let rate = description.pointee.mSampleRate
        let count = CMSampleBufferGetNumSamples(sample)
        guard rate > 0, count > 0 else { return }
        if !nextAudioTime.isValid {
            // Use the beginning of this PCM buffer on the host clock. Device
            // clocks and delayed/pre-roll buffers must not shift audio ahead.
            nextAudioTime = CMTimeMaximum(.zero, CMTimeSubtract(time(host), CMTime(seconds: Double(count)/rate, preferredTimescale: 60_000)))
        }
        let start = nextAudioTime
        let step = CMTime(seconds: 1 / rate, preferredTimescale: Int32(rate))
        nextAudioTime = CMTimeAdd(start, CMTimeMultiply(step, multiplier: Int32(count)))
        // Preserve time when backpressure drops a buffer; never compact audio.
        guard ready(input) else { return }
        var timing = CMSampleTimingInfo(duration: step, presentationTimeStamp: start, decodeTimeStamp: .invalid)
        var copy: CMSampleBuffer?
        let status = CMSampleBufferCreateCopyWithNewTiming(allocator: kCFAllocatorDefault, sampleBuffer: sample,
            sampleTimingEntryCount: 1, sampleTimingArray: &timing, sampleBufferOut: &copy)
        guard status == noErr, let copy, input.append(copy) else {
            fail(writer?.error?.localizedDescription ?? "cannot append USB audio")
        }
        audioSamples += count
    }

    func finish(at host: Double) {
        guard !finishing else { return }
        finishing = true
        ticker?.cancel(); ticker = nil
        guard let w = writer, let input = videoInput, let image = lastImage, lastVideoTime.isValid else {
            fail("USB capture produced no video frames")
        }
        if audio && audioSamples == 0 { fail("USB capture produced no audio samples") }
        let tick = CMTime(value: 1, timescale: 60_000)
        let end = CMTimeMaximum(time(host), CMTimeAdd(lastVideoTime, tick))
        let finalPTS = CMTimeSubtract(end, tick)
        let deadline = hostSeconds() + 5
        // Hold the last frame through stop, including a completely static tail.
        func complete() {
            if CMTimeCompare(finalPTS, self.lastVideoTime) > 0 {
                guard input.isReadyForMoreMediaData else {
                    if hostSeconds() >= deadline { fail("USB video encoder did not finish in time") }
                    self.queue.asyncAfter(deadline: .now() + 0.01) { complete() }
                    return
                }
                guard self.adaptor?.append(image, withPresentationTime: finalPTS) == true else {
                    fail(w.error?.localizedDescription ?? "cannot finish USB video")
                }
            }
            w.endSession(atSourceTime: end)
            input.markAsFinished(); self.audioInput?.markAsFinished()
            w.finishWriting {
                guard w.status == .completed else { fail(w.error?.localizedDescription ?? "cannot finalize USB movie") }
                self.onFinished?()
                print("DONE"); exit(0)
            }
        }
        complete()
    }
}

// Exercise the actual movie writer with discontinuous device PTS, sparse video
// and a long static tail. This needs neither a phone nor Camera permission.
func selftest(url: URL, seconds: Double, audio: Bool) {
    let recorder = Recorder(url: url, audio: audio, realtime: false)
    var image: CVPixelBuffer?
    guard CVPixelBufferCreate(kCFAllocatorDefault, 160, 240, kCVPixelFormatType_32BGRA, nil, &image) == kCVReturnSuccess,
          let image else { fail("cannot create regression frame") }
    CVPixelBufferLockBaseAddress(image, [])
    memset(CVPixelBufferGetBaseAddress(image), 100, CVPixelBufferGetDataSize(image))
    CVPixelBufferUnlockBaseAddress(image, [])
    var videoFormat: CMVideoFormatDescription?
    CMVideoFormatDescriptionCreateForImageBuffer(allocator: kCFAllocatorDefault, imageBuffer: image, formatDescriptionOut: &videoFormat)
    var asbd = AudioStreamBasicDescription(mSampleRate: 48_000, mFormatID: kAudioFormatLinearPCM,
        mFormatFlags: kAudioFormatFlagIsSignedInteger | kAudioFormatFlagIsPacked,
        mBytesPerPacket: 4, mFramesPerPacket: 1, mBytesPerFrame: 4, mChannelsPerFrame: 2, mBitsPerChannel: 16, mReserved: 0)
    var audioFormat: CMAudioFormatDescription?
    CMAudioFormatDescriptionCreate(allocator: kCFAllocatorDefault, asbd: &asbd, layoutSize: 0, layout: nil,
        magicCookieSize: 0, magicCookie: nil, extensions: nil, formatDescriptionOut: &audioFormat)
    let steps = Int((seconds * 10).rounded())
    for i in 0..<steps {
        let host = Double(i) / 10
        // Simulate a device clock jumping backward midway through recording.
        let deviceTime = CMTime(seconds: i < steps/2 ? host + 1000 : host + 500, preferredTimescale: 60_000)
        if audio || i == 0 || i == steps/4 || i == steps/2 {
            var timing = CMSampleTimingInfo(duration: .invalid, presentationTimeStamp: deviceTime, decodeTimeStamp: .invalid)
            var sample: CMSampleBuffer?
            CMSampleBufferCreateReadyWithImageBuffer(allocator: kCFAllocatorDefault, imageBuffer: image,
                formatDescription: videoFormat!, sampleTiming: &timing, sampleBufferOut: &sample)
            recorder.writeVideo(sample!, at: host)
        }
        if audio {
            var block: CMBlockBuffer?
            CMBlockBufferCreateWithMemoryBlock(allocator: kCFAllocatorDefault, memoryBlock: nil, blockLength: 19_200,
                blockAllocator: kCFAllocatorDefault, customBlockSource: nil, offsetToData: 0, dataLength: 19_200, flags: 0, blockBufferOut: &block)
            CMBlockBufferFillDataBytes(with: 0, blockBuffer: block!, offsetIntoDestination: 0, dataLength: 19_200)
            var timing = CMSampleTimingInfo(duration: CMTime(value: 1, timescale: 48_000), presentationTimeStamp: deviceTime, decodeTimeStamp: .invalid)
            var size = 4
            var sample: CMSampleBuffer?
            CMSampleBufferCreateReady(allocator: kCFAllocatorDefault, dataBuffer: block, formatDescription: audioFormat,
                sampleCount: 4800, sampleTimingEntryCount: 1, sampleTimingArray: &timing,
                sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &sample)
            recorder.writeAudio(sample!, at: host)
        }
    }
    recorder.finish(at: seconds)
    RunLoop.main.run()
}

var arguments = Array(CommandLine.arguments.dropFirst())
guard let command = arguments.first else { fail("usage: ioscapture list | record --udid <udid> --out <file>") }
arguments.removeFirst()

func option(_ name: String) -> String? {
    guard let i = arguments.firstIndex(of: name), i + 1 < arguments.count else { return nil }
    return arguments[i + 1]
}

switch command {
case "selftest":
    guard let out = option("--out"), let seconds = Double(option("--seconds") ?? "18"), (0.5...30).contains(seconds) else { fail("selftest needs --out and --seconds 0.5...30") }
    try? FileManager.default.removeItem(atPath: out)
    selftest(url: URL(fileURLWithPath: out), seconds: seconds, audio: !arguments.contains("--no-audio"))
case "list":
    enableScreenCaptureDevices()
    let devices = waitForDevices(matching: nil, seconds: 3)
    let rows = devices.map { ["id": $0.uniqueID, "name": $0.localizedName, "model": $0.modelID] }
    let data = try! JSONSerialization.data(withJSONObject: rows)
    print(String(data: data, encoding: .utf8)!)
    exit(0)

case "record":
    enableScreenCaptureDevices()
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
    let video = AVCaptureVideoDataOutput()
    video.alwaysDiscardsLateVideoFrames = true
    let preferred = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange
    if video.availableVideoPixelFormatTypes.contains(preferred) {
        video.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: preferred]
    }
    guard session.canAddOutput(video) else { fail("cannot read USB video samples") }
    session.addOutput(video)
    let audio = !arguments.contains("--no-audio")
    let recorder = Recorder(url: URL(fileURLWithPath: out), audio: audio)
    video.setSampleBufferDelegate(recorder, queue: recorder.ingress)
    if audio {
        let output = AVCaptureAudioDataOutput()
        guard session.canAddOutput(output) else { fail("cannot read USB audio samples") }
        session.addOutput(output)
        output.setSampleBufferDelegate(recorder, queue: recorder.ingress)
    }
    try? FileManager.default.removeItem(atPath: out)
    recorder.onFinished = { session.stopRunning() }
    session.startRunning()

    var sources: [DispatchSourceSignal] = []
    for sig in [SIGINT, SIGTERM] {
        signal(sig, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: sig, queue: DispatchQueue.global(qos: .userInitiated))
        source.setEventHandler {
            recorder.requestStop(at: hostSeconds())
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

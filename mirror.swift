import AppKit
import ApplicationServices
import ScreenCaptureKit
import VideoToolbox

enum MirrorError: Error {
    case permission, window, coordinates, encode
}

final class FrameBuffer: NSObject, SCStreamOutput, @unchecked Sendable {
    private let lock = NSLock()
    private var pendingImage: CGImage?

    func stream(_ stream: SCStream, didOutputSampleBuffer sampleBuffer: CMSampleBuffer,
                of type: SCStreamOutputType) {
        guard type == .screen, let pixelBuffer = sampleBuffer.imageBuffer else { return }
        var image: CGImage?
        guard VTCreateCGImageFromCVPixelBuffer(pixelBuffer, options: nil, imageOut: &image) == noErr,
              let image else { return }
        lock.withLock { pendingImage = image }
    }

    func take() -> CGImage? {
        lock.withLock {
            defer { pendingImage = nil }
            return pendingImage
        }
    }
}

@main struct Mirror {
    @MainActor static func main() async {
        do {
            try await run()
        } catch {
            FileHandle.standardError.write(Data("mirror: \(error)\n".utf8))
            exit(1)
        }
    }

    @MainActor static func run() async throws {
        _ = NSApplication.shared
        let options = Array(CommandLine.arguments.dropFirst())
        let wechat = options.first == "--wechat"
        let args = wechat ? Array(options.dropFirst()) : options
        let bundle = wechat ? "com.tencent.xinWeChat" : "com.apple.ScreenContinuity"
        guard CGPreflightScreenCaptureAccess(), let command = args.first else {
            throw MirrorError.permission
        }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        guard let window = content.windows.filter({
            $0.owningApplication?.bundleIdentifier == bundle && $0.frame.height > 400 &&
            (!wechat || $0.title == "砖了个砖")
        }).max(by: { $0.frame.height < $1.frame.height }) else {
            throw MirrorError.window
        }
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let config = SCStreamConfiguration()
        config.width = Int(window.frame.width)
        config.height = Int(window.frame.height)
        config.ignoreShadowsSingleWindow = true
        config.showsCursor = false

        if command == "serve", args.count == 1, wechat {
            try await serve(window: window, bundle: bundle) {
                try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
            }
        } else if command == "serve", args.count == 1 {
            config.minimumFrameInterval = CMTime(value: 1, timescale: 24)
            config.queueDepth = 5
            let frames = FrameBuffer()
            let stream = SCStream(filter: filter, configuration: config, delegate: nil)
            try stream.addStreamOutput(frames, type: .screen, sampleHandlerQueue: DispatchQueue(label: "mirror.frames"))
            try await stream.startCapture()
            try await serve(window: window, bundle: bundle) { frames.take() }
            try await stream.stopCapture()
        } else if command == "frame", args.count == 2 {
            let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
            try png(image).write(to: URL(fileURLWithPath: args[1]))
        } else {
            try await send(args, window: window, bundle: bundle)
        }
    }

    static func png(_ image: CGImage) throws -> Data {
        guard let data = NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:]) else {
            throw MirrorError.encode
        }
        return data
    }

    @MainActor static func serve(window: SCWindow, bundle: String,
                                  takeFrame: () async throws -> CGImage?) async throws {
        let output = FileHandle.standardOutput
        while let line = readLine() {
            let args = line.split(separator: " ").map(String.init)
            if args == ["frame"] || args == ["latest"] {
                if let image = try await takeFrame() {
                    let data = try png(image)
                    try output.write(contentsOf: Data("FRAME \(data.count)\n".utf8))
                    try output.write(contentsOf: data)
                } else {
                    try output.write(contentsOf: Data("WAIT\n".utf8))
                }
            } else {
                try await send(args, window: window, bundle: bundle)
                try output.write(contentsOf: Data("OK\n".utf8))
            }
        }
    }

    @MainActor static func send(_ args: [String], window: SCWindow, bundle: String) async throws {
        guard CGPreflightPostEventAccess() else { throw MirrorError.permission }
        guard let command = args.first else { throw MirrorError.coordinates }
        let coordinates = args.dropFirst().compactMap(Double.init)
        guard (command == "click" && args.count == 3 && coordinates.count == 2) ||
              (["click-pair", "drag"].contains(command) && args.count == 5 && coordinates.count == 4) else {
            throw MirrorError.coordinates
        }

        if NSWorkspace.shared.frontmostApplication?.bundleIdentifier != bundle {
            guard let running = NSRunningApplication.runningApplications(withBundleIdentifier: bundle).first else {
                throw MirrorError.window
            }
            _ = running.activate()
            for _ in 0..<12 {
                if NSWorkspace.shared.frontmostApplication?.bundleIdentifier == bundle { break }
                try await Task.sleep(for: .milliseconds(50))
            }
        }
        guard NSWorkspace.shared.frontmostApplication?.bundleIdentifier == bundle,
              let info = CGWindowListCopyWindowInfo(.optionIncludingWindow, window.windowID) as? [[String: Any]],
              let bounds = info.first?[kCGWindowBounds as String] as? NSDictionary,
              let frame = CGRect(dictionaryRepresentation: bounds),
              frame.size == window.frame.size else { throw MirrorError.window }
        let start = CGPoint(x: frame.minX + coordinates[0], y: frame.minY + coordinates[1])
        let end = command == "click" ? start : CGPoint(x: frame.minX + coordinates[2], y: frame.minY + coordinates[3])
        guard frame.contains(start), frame.contains(end) else { throw MirrorError.coordinates }

        let source = CGEventSource(stateID: .hidSystemState)
        func post(_ type: CGEventType, _ point: CGPoint) {
            CGEvent(mouseEventSource: source, mouseType: type, mouseCursorPosition: point, mouseButton: .left)?.post(tap: .cghidEventTap)
        }
        func click(_ point: CGPoint) {
            post(.mouseMoved, point)
            usleep(30_000)
            post(.leftMouseDown, point)
            usleep(50_000)
            post(.leftMouseUp, point)
        }
        if command == "click" {
            click(start)
        } else if command == "click-pair" {
            click(start)
            usleep(80_000)
            click(end)
        } else {
            post(.mouseMoved, start)
            usleep(30_000)
            post(.leftMouseDown, start)
            usleep(80_000)
            for step in 1...14 {
                let t = CGFloat(step) / 14
                post(.leftMouseDragged, CGPoint(x: start.x + (end.x - start.x) * t, y: start.y + (end.y - start.y) * t))
                usleep(20_000)
            }
            usleep(80_000)
            post(.leftMouseUp, end)
        }
    }
}

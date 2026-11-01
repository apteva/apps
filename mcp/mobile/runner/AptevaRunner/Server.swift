import Foundation
import Network

struct HTTPRequest {
    let method: String
    let path: String
    let query: [String: String]
    let body: Data

    func json() -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: body)) as? [String: Any] ?? [:]
    }
}

struct HTTPResponse {
    var status = 200
    var contentType = "application/json"
    var body = Data()

    static func json(_ object: Any, status: Int = 200) -> HTTPResponse {
        let data = (try? JSONSerialization.data(withJSONObject: object)) ?? Data("{}".utf8)
        return HTTPResponse(status: status, body: data)
    }

    static func error(_ status: Int, _ code: String, _ message: String) -> HTTPResponse {
        json(["error": code, "message": message], status: status)
    }
}

/// A deliberately small HTTP/1.1 server: one request per connection, bodies
/// sized by Content-Length. The runner is reached only through the USB mux
/// (devices) or loopback (simulators), so there is no TLS or auth here.
final class HTTPServer {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "apteva.runner.http", attributes: .concurrent)
    private let handler: (HTTPRequest) -> HTTPResponse
    private static let maxRequest = 8 << 20

    init(port: UInt16, handler: @escaping (HTTPRequest) -> HTTPResponse) throws {
        let params = NWParameters.tcp
        params.allowLocalEndpointReuse = true
        guard let p = NWEndpoint.Port(rawValue: port) else { throw URLError(.badURL) }
        listener = try NWListener(using: params, on: p)
        self.handler = handler
    }

    func start() {
        listener.newConnectionHandler = { [weak self] connection in self?.serve(connection) }
        listener.start(queue: queue)
    }

    func stop() { listener.cancel() }

    private func serve(_ connection: NWConnection) {
        connection.start(queue: queue)
        receive(connection, Data())
    }

    private func receive(_ connection: NWConnection, _ buffer: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 1 << 16) { [weak self] data, _, done, error in
            guard let self else { return }
            var buffer = buffer
            if let data { buffer.append(data) }
            if let request = HTTPServer.parse(buffer) {
                self.send(connection, self.handler(request))
                return
            }
            if done || error != nil || buffer.count > HTTPServer.maxRequest {
                connection.cancel()
                return
            }
            self.receive(connection, buffer)
        }
    }

    private func send(_ connection: NWConnection, _ response: HTTPResponse) {
        let head = "HTTP/1.1 \(response.status) \(response.status < 400 ? "OK" : "Error")\r\nContent-Type: \(response.contentType)\r\nContent-Length: \(response.body.count)\r\nConnection: close\r\n\r\n"
        var out = Data(head.utf8)
        out.append(response.body)
        connection.send(content: out, completion: .contentProcessed { _ in connection.cancel() })
    }

    static func parse(_ buffer: Data) -> HTTPRequest? {
        guard let end = buffer.range(of: Data("\r\n\r\n".utf8)) else { return nil }
        let head = String(decoding: buffer[buffer.startIndex..<end.lowerBound], as: UTF8.self)
        var lines = head.components(separatedBy: "\r\n")
        let first = lines.removeFirst().split(separator: " ")
        guard first.count >= 2 else { return HTTPRequest(method: "BAD", path: "/", query: [:], body: Data()) }
        var length = 0
        for line in lines {
            let parts = line.split(separator: ":", maxSplits: 1)
            if parts.count == 2, parts[0].lowercased() == "content-length" {
                length = Int(parts[1].trimmingCharacters(in: .whitespaces)) ?? 0
            }
        }
        let start = end.upperBound
        guard length >= 0, buffer.endIndex - start >= length else { return nil }
        let body = buffer.subdata(in: start..<(start + length))
        let components = URLComponents(string: String(first[1]))
        var query: [String: String] = [:]
        for item in components?.queryItems ?? [] { query[item.name] = item.value ?? "" }
        return HTTPRequest(method: String(first[0]), path: components?.path ?? "/", query: query, body: body)
    }
}

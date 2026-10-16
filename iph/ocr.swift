// iph-ocr: persistent Vision OCR, kept warm by `iph flow` (~35 ms a frame vs
// ~160 ms cold). Protocol on stdin: a 4-byte big-endian length, then that many
// bytes of JPEG/PNG. One JSON line out per image:
// [{"t":text,"x":,"y":,"w":,"h":}] in image pixels, top-left origin.
// No temp files: frames come straight from the phone's MJPEG stream.
import AppKit
import Foundation
import Vision

setvbuf(stdout, nil, _IOLBF, 0)
let input = FileHandle.standardInput

func readExactly(_ n: Int) -> Data? {
    var d = Data()
    while d.count < n {
        let chunk = input.readData(ofLength: n - d.count)
        if chunk.isEmpty { return nil }
        d.append(chunk)
    }
    return d
}

while let hdr = readExactly(4) {
    let n = hdr.reduce(0) { $0 << 8 | Int($1) }
    guard let bytes = readExactly(n) else { break }
    guard let src = CGImageSourceCreateWithData(bytes as CFData, nil),
          let img = CGImageSourceCreateImageAtIndex(src, 0, nil) else { print("[]"); continue }
    let W = Double(img.width), H = Double(img.height)
    let r = VNRecognizeTextRequest()
    r.recognitionLevel = .fast
    r.usesLanguageCorrection = false
    try? VNImageRequestHandler(cgImage: img).perform([r])
    var out: [[String: Any]] = []
    for o in r.results ?? [] {
        guard let s = o.topCandidates(1).first?.string else { continue }
        let b = o.boundingBox
        out.append(["t": s, "x": Int(b.minX * W), "y": Int((1 - b.maxY) * H), "w": Int(b.width * W), "h": Int(b.height * H)])
    }
    print(String(data: try! JSONSerialization.data(withJSONObject: out), encoding: .utf8)!)
}

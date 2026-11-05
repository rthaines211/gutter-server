// THROWAWAY spike: can Apple Vision find speech bubbles + reading order on our comics?
// usage: swift bubbles.swift <pagesDir> <outDir>
import Foundation
import Vision
import CoreGraphics
import ImageIO
import CoreText
import UniformTypeIdentifiers

struct Line { var r: CGRect; var text: String; var conf: Float }   // r in pixel coords, top-left origin
struct Bubble { var r: CGRect; var lines: [Line]; var regions: [Int] = [] ; var region: Int { regions.first ?? 0 } }

func loadImage(_ url: URL) -> CGImage? {
  guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
  return CGImageSourceCreateImageAtIndex(src, 0, nil)
}

func detect(_ img: CGImage, fast: Bool) throws -> [Line] {
  let req = VNRecognizeTextRequest()
  req.recognitionLevel = fast ? .fast : .accurate
  req.usesLanguageCorrection = false
  try VNImageRequestHandler(cgImage: img).perform([req])
  let W = CGFloat(img.width), H = CGFloat(img.height)
  return (req.results ?? []).compactMap { o in
    guard let c = o.topCandidates(1).first else { return nil }
    let b = o.boundingBox
    let r = CGRect(x: b.minX * W, y: (1 - b.maxY) * H, width: b.width * W, height: b.height * H)
    return Line(r: r, text: c.string, conf: c.confidence)
  }
}

// Merge lines into bubbles: two lines belong together when they're vertically
// close (gap < ~1 line height) and horizontally overlapping / centred near each other.
func cluster(_ lines: [Line]) -> [Bubble] {
  var parent = Array(0..<lines.count)
  func find(_ i: Int) -> Int { var i = i; while parent[i] != i { parent[i] = parent[parent[i]]; i = parent[i] }; return i }
  for i in lines.indices { for j in lines.indices where j > i {
    let a = lines[i].r, b = lines[j].r
    let lh = max(min(a.height, b.height), 1)
    let vgap = max(0, max(a.minY, b.minY) - min(a.maxY, b.maxY))
    let hoverlap = min(a.maxX, b.maxX) - max(a.minX, b.minX)
    let centreDx = abs(a.midX - b.midX)
    let sameSize = max(a.height, b.height) / lh < 1.8
    if sameSize && vgap < lh * 1.1 && (hoverlap > 0 || centreDx < lh * 2) { parent[find(i)] = find(j) }
  }}
  var groups: [Int: [Line]] = [:]
  for i in lines.indices { groups[find(i), default: []].append(lines[i]) }
  return groups.values.map { ls in Bubble(r: ls.map(\.r).reduce(ls[0].r) { $0.union($1) }, lines: ls) }
}

// Western reading order: band bubbles into rows by vertical overlap, rows top→bottom, L→R within a row.
func order(_ bs: [Bubble]) -> [Bubble] {
  var rows: [[Bubble]] = []
  for b in bs.sorted(by: { $0.r.minY < $1.r.minY }) {
    if let i = rows.lastIndex(where: { row in row.contains { o in
      let ov = min(o.r.maxY, b.r.maxY) - max(o.r.minY, b.r.minY)
      return ov > min(o.r.height, b.r.height) * 0.4 } }) { rows[i].append(b) } else { rows.append([b]) }
  }
  return rows.flatMap { $0.sorted { $0.r.minX < $1.r.minX } }
}


// ---- panels: recursive XY-cut on gutters (page background colour) ----
struct Gray { let w: Int, h: Int, px: [UInt8] ; subscript(x: Int, y: Int) -> UInt8 { px[y * w + x] } }
func gray(_ img: CGImage, width: Int) -> Gray {
  let h = Int(Double(img.height) * Double(width) / Double(img.width))
  var px = [UInt8](repeating: 0, count: width * h)
  let ctx = CGContext(data: &px, width: width, height: h, bitsPerComponent: 8, bytesPerRow: width,
                      space: CGColorSpaceCreateDeviceGray(), bitmapInfo: 0)!
  ctx.interpolationQuality = .medium
  ctx.draw(img, in: CGRect(x: 0, y: 0, width: width, height: h))
  return Gray(w: width, h: h, px: px)   // row 0 = top
}
func panels(_ img: CGImage, mask: [CGRect] = []) -> [CGRect] {
  let g0 = gray(img, width: 800)
  let k0 = CGFloat(g0.w) / CGFloat(img.width)
  var g = g0
  // background = median of the outer 2px frame
  var edge: [UInt8] = []
  for x in 0..<g.w { edge += [g[x, 0], g[x, 1], g[x, g.h - 1], g[x, g.h - 2]] }
  for y in 0..<g.h { edge += [g[0, y], g[1, y], g[g.w - 1, y], g[g.w - 2, y]] }
  var bg = Int(edge.sorted()[edge.count / 2])
  var best: [CGRect] = []
  for cand in [bg, 255, 0] { bg = cand
  var px = g0.px
  for m in mask {   // ponytail: paint balloons as gutter so the cut can pass through them
    let x0 = max(0, Int(m.minX * k0)), x1 = min(g0.w, Int(m.maxX * k0) + 1), y0 = max(0, Int(m.minY * k0)), y1 = min(g0.h, Int(m.maxY * k0) + 1)
    if x1 > x0 && y1 > y0 { for y in y0..<y1 { for x in x0..<x1 { px[y * g0.w + x] = UInt8(bg) } } }
  }
  g = Gray(w: g0.w, h: g0.h, px: px)
  func isBg(_ x: Int, _ y: Int) -> Bool { abs(Int(g[x, y]) - bg) < 45 }
  let minGap = 2, minSize = 50
  // split [lo,hi) along one axis into non-gutter segments
  func segments(_ r: (x0: Int, y0: Int, x1: Int, y1: Int), horizontal: Bool) -> [(Int, Int)] {
    let (lo, hi) = horizontal ? (r.y0, r.y1) : (r.x0, r.x1)
    var gut = [Bool](repeating: false, count: hi - lo)
    for i in lo..<hi {
      var n = 0, t = 0
      if horizontal { for x in r.x0..<r.x1 { t += 1; if isBg(x, i) { n += 1 } } }
      else { for y in r.y0..<r.y1 { t += 1; if isBg(i, y) { n += 1 } } }
      gut[i - lo] = Double(n) / Double(max(t, 1)) > 0.95
    }
    var segs: [(Int, Int)] = [], i = 0
    while i < gut.count {
      if gut[i] { i += 1; continue }
      var j = i; while j < gut.count {
        if gut[j] { var k = j; while k < gut.count && gut[k] { k += 1 }; if k - j >= minGap || k == gut.count { break }; j = k } else { j += 1 }
      }
      if j - i >= minSize { segs.append((lo + i, lo + j)) }
      i = j
    }
    return segs
  }
  var out: [CGRect] = []
  func cut(_ r: (x0: Int, y0: Int, x1: Int, y1: Int), depth: Int) {
    for horizontal in [true, false] {
      let segs = segments(r, horizontal: horizontal)
      if segs.count > 1 && depth < 8 {
        for s in segs { cut(horizontal ? (r.x0, s.0, r.x1, s.1) : (s.0, r.y0, s.1, r.y1), depth: depth + 1) }
        return
      }
    }
    let k = CGFloat(img.width) / CGFloat(g.w)
    out.append(CGRect(x: CGFloat(r.x0) * k, y: CGFloat(r.y0) * k, width: CGFloat(r.x1 - r.x0) * k, height: CGFloat(r.y1 - r.y0) * k))
  }
  cut((0, 0, g.w, g.h), depth: 0)
  if out.count > best.count { best = out }
  }
  return best   // already in western reading order (rows top→bottom, then L→R)
}

// Bubble interiors are flat: sample a thin ring just OUTSIDE the text block. In a balloon/caption
// that ring is the fill colour (one tone); for signs / SFX it's art (many tones).
func flatness(_ g: Gray, _ r: CGRect, pad: CGFloat, scale: CGFloat) -> Double {
  let o = r.insetBy(dx: -pad, dy: -pad)
  let x0 = max(0, Int(o.minX * scale)), x1 = min(g.w, Int(o.maxX * scale))
  let y0 = max(0, Int(o.minY * scale)), y1 = min(g.h, Int(o.maxY * scale))
  let ix0 = Int(r.minX * scale), ix1 = Int(r.maxX * scale), iy0 = Int(r.minY * scale), iy1 = Int(r.maxY * scale)
  guard x1 > x0, y1 > y0 else { return 0 }
  var hist = [Int](repeating: 0, count: 32)
  var t = 0
  for y in y0..<y1 {
    for x in x0..<x1 where !(x >= ix0 && x < ix1 && y >= iy0 && y < iy1) {
      hist[Int(g[x, y]) / 8] += 1
      t += 1
    }
  }
  guard t > 0 else { return 0 }
  let peak = hist.indices.max { hist[$0] < hist[$1] }!
  var near = 0
  for i in max(0, peak - 1)...min(31, peak + 1) { near += hist[i] }
  return Double(near) / Double(t)
}

func orderByPanels(_ bs: [Bubble], _ ps: [CGRect]) -> [Bubble] {
  func panelIndex(_ b: Bubble) -> Int {
    let c = CGPoint(x: b.r.midX, y: b.r.midY)
    if let i = ps.firstIndex(where: { $0.contains(c) }) { return i }
    return ps.indices.min { hypot(ps[$0].midX - c.x, ps[$0].midY - c.y) < hypot(ps[$1].midX - c.x, ps[$1].midY - c.y) } ?? 0
  }
  return Dictionary(grouping: bs, by: panelIndex).sorted { $0.key < $1.key }.flatMap { order($0.value) }
}

// ---- balloons: tone flood fill with the text painted solid and a morphological opening ----
// Walls are pixels that differ from the ring tone by > 30 (as before). New: the text box is painted as
// fill so the interior is solid, the passable mask is eroded by K px so gutters (thin) and small outline
// gaps vanish, the fill runs on the eroded mask, then the region is grown back by K inside the mask.
// A balloon sitting on a white gutter no longer runs down the gutter to the page edge.
let K = 6
func erode(_ m: [Bool], _ w: Int, _ h: Int, _ k: Int) -> [Bool] {
  var t = [Bool](repeating: false, count: w * h), o = [Bool](repeating: false, count: w * h)
  for y in 0..<h { var run = 0; for x in 0..<w { run = m[y * w + x] ? run + 1 : 0; if run >= 2 * k + 1 { t[y * w + x - k] = true } } }
  for x in 0..<w { var run = 0; for y in 0..<h { run = t[y * w + x] ? run + 1 : 0; if run >= 2 * k + 1 { o[(y - k) * w + x] = true } } }
  return o
}
func balloons(_ g: Gray, _ lines: [Line], scale: CGFloat, boxes: [CGRect]) -> (bubbles: [Bubble], label: [Int32]) {
  let W = g.w, H = g.h
  var label = [Int32](repeating: 0, count: W * H)
  struct Reg { var area = 0, x0 = Int.max, y0 = Int.max, x1 = 0, y1 = 0, leaked = false }
  var regs: [Reg] = [Reg()]
  func px(_ r: CGRect) -> (Int, Int, Int, Int) { (max(0, Int(r.minX * scale)), max(0, Int(r.minY * scale)), min(W, Int(r.maxX * scale)), min(H, Int(r.maxY * scale))) }
  // text painted solid (as in round 2) so the fill crosses the lettering
  var text = [Bool](repeating: false, count: W * H)
  func paint(_ x0: Int, _ y0: Int, _ x1: Int, _ y1: Int) { if x1 > x0 && y1 > y0 { for y in y0..<y1 { for x in x0..<x1 { text[y * W + x] = true } } } }
  for ls in cluster(lines).map({ $0.lines.sorted { $0.r.minY < $1.r.minY } }) {
    for (i, l) in ls.enumerated() {
      let ddx = min(l.r.height * 0.1, max(0, (l.r.width - 1) / 2))
      let (x0, y0, x1, y1) = px(l.r.insetBy(dx: ddx, dy: 0)); paint(x0, y0, x1, y1)
      if i + 1 < ls.count { let n = ls[i + 1].r
        if n.minY - l.r.maxY < l.r.height * 1.5 {
          let (bx0, by0, bx1, by1) = px(CGRect(x: max(l.r.minX, n.minX), y: l.r.maxY, width: min(l.r.maxX, n.maxX) - max(l.r.minX, n.minX), height: max(0, n.minY - l.r.maxY)))
          paint(bx0, by0, bx1, by1) } }
    }
  }
  // assign each Vision line to the detector box containing its centre (smallest box wins); lines outside any box are dropped
  var boxLines = [[Line]](repeating: [], count: boxes.count)
  for l in lines {
    let c = CGPoint(x: l.r.midX, y: l.r.midY)
    let hits = boxes.indices.filter { boxes[$0].insetBy(dx: -l.r.height * 0.5, dy: -l.r.height * 0.5).contains(c) }
    if let b = hits.min(by: { boxes[$0].width * boxes[$0].height < boxes[$1].width * boxes[$1].height }) { boxLines[b].append(l) }
  }
  var out: [Bubble] = []
  for (bi, box) in boxes.enumerated() {
    let ls = boxLines[bi]
    let (bx0, by0, bx1, by1) = px(box.insetBy(dx: -3, dy: -3))
    guard bx1 > bx0 + 2, by1 > by0 + 2 else { continue }
    // seed + tone: ring around the widest line, else the box centre
    var seedX = (bx0 + bx1) / 2, seedY = (by0 + by1) / 2
    var hist = [Int](repeating: 0, count: 32)
    if let l = ls.max(by: { $0.r.width < $1.r.width }) {
      let pad = l.r.height * 0.35
      let (x0, y0, x1, y1) = px(l.r.insetBy(dx: -pad, dy: -pad)); let (ix0, iy0, ix1, iy1) = px(l.r)
      if y1 > y0 && x1 > x0 { for y in y0..<y1 { for x in x0..<x1 where !(x >= ix0 && x < ix1 && y >= iy0 && y < iy1) { hist[Int(g[x, y]) / 8] += 1 } } }
      seedX = min(max((ix0 + ix1) / 2, bx0), bx1 - 1); seedY = min(max((iy0 + iy1) / 2, by0), by1 - 1)
    } else {
      // no text: sample the box interior, take the dominant light tone
      for y in by0..<by1 { for x in bx0..<bx1 { hist[Int(g[x, y]) / 8] += 1 } }
    }
    let peak = hist.indices.max { hist[$0] < hist[$1] }!; let v0 = peak * 8 + 4
    var r = Reg(); let id = Int32(regs.count)
    var done = false
    for K in [5, 3, 1] where !done {
      var pass = [Bool](repeating: false, count: W * H)
      for y in by0..<by1 { for x in bx0..<bx1 { let q = y * W + x; pass[q] = text[q] || abs(Int(g.px[q]) - v0) <= 30 } }
      let er = erode(pass, W, H, K)
      // seed: the eroded pixel nearest the seed point
      var seed = -1; var best = Int.max
      let sy0 = max(by0, seedY - 40), sy1 = min(by1, seedY + 40), sx0 = max(bx0, seedX - 40), sx1 = min(bx1, seedX + 40)
      if sy1 > sy0 && sx1 > sx0 { for y in sy0..<sy1 { for x in sx0..<sx1 where er[y * W + x] {
        let d = (x - seedX) * (x - seedX) + (y - seedY) * (y - seedY); if d < best { best = d; seed = y * W + x } } } }
      guard seed >= 0 else { continue }
      done = true
      var stack = [seed]; var core: [Int] = []; var visited = Set<Int>([seed])
      while let p = stack.popLast() { core.append(p); let x = p % W, y = p / W
        for q in [p - 1, p + 1, p - W, p + W] where q >= 0 && q < W * H && er[q] && !visited.contains(q) { let qx = q % W, qy = q / W; if qx >= bx0 && qx < bx1 && qy >= by0 && qy < by1 { visited.insert(q); stack.append(q) } } }
      var grown = Set<Int>()
      for p in core { let x = p % W, y = p / W
        for dy in -K...K { for dx in -K...K { let qx = x + dx, qy = y + dy
          if qx >= bx0 && qy >= by0 && qx < bx1 && qy < by1 { let q = qy * W + qx; if pass[q] && label[q] == 0 { grown.insert(q) } } } } }
      for q in grown { let x = q % W, y = q / W; r.area += 1; r.x0 = min(r.x0, x); r.x1 = max(r.x1, x); r.y0 = min(r.y0, y); r.y1 = max(r.y1, y); label[q] = id }
    }
    regs.append(r)
    let rect = r.area > 0 ? CGRect(x: CGFloat(r.x0) / scale, y: CGFloat(r.y0) / scale, width: CGFloat(r.x1 - r.x0 + 1) / scale, height: CGFloat(r.y1 - r.y0 + 1) / scale) : box
    out.append(Bubble(r: ls.map(\.r).reduce(rect) { $0.union($1) }, lines: ls, regions: r.area > 0 ? [Int(id)] : []))
  }
  return (out, label)
}

// Merge boxes that mostly overlap (nested / split balloons): intersection >= half the smaller box.
func mergeOverlaps(_ bs: [Bubble]) -> [Bubble] {
  var bs = bs
  var changed = true
  while changed {
    changed = false
    outer: for i in bs.indices { for j in bs.indices where j > i {
      let inter = bs[i].r.intersection(bs[j].r)
      if inter.isNull { continue }
      let a = inter.width * inter.height, small = min(bs[i].r.width * bs[i].r.height, bs[j].r.width * bs[j].r.height)
      if a >= 0.5 * small {
        bs[i] = Bubble(r: bs[i].r.union(bs[j].r), lines: bs[i].lines + bs[j].lines, regions: bs[i].regions + bs[j].regions)
        bs.remove(at: j); changed = true; break outer
      }
    } }
  }
  return bs
}

func writeLabels(_ g: Gray, _ label: [Int32], _ bs: [Bubble], to url: URL) {
  var idx = [Int32: UInt8](); for (i, b) in bs.enumerated() { for rg in b.regions { idx[Int32(rg)] = UInt8(min(i + 1, 255)) } }
  var px = label.map { idx[$0] ?? 0 }
  let ctx = CGContext(data: &px, width: g.w, height: g.h, bitsPerComponent: 8, bytesPerRow: g.w,
                      space: CGColorSpaceCreateDeviceGray(), bitmapInfo: 0)!
  let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
  CGImageDestinationAddImage(dest, ctx.makeImage()!, nil); CGImageDestinationFinalize(dest)
}

func render(_ img: CGImage, _ bs: [Bubble], _ lines: [Line], _ ps: [CGRect], to url: URL) {
  let scale = 1000.0 / CGFloat(img.width)
  let W = Int(CGFloat(img.width) * scale), H = Int(CGFloat(img.height) * scale)
  let ctx = CGContext(data: nil, width: W, height: H, bitsPerComponent: 8, bytesPerRow: 0,
                      space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
  ctx.draw(img, in: CGRect(x: 0, y: 0, width: W, height: H))
  ctx.translateBy(x: 0, y: CGFloat(H)); ctx.scaleBy(x: 1, y: -1)   // top-left origin
  func s(_ r: CGRect) -> CGRect { CGRect(x: r.minX * scale, y: r.minY * scale, width: r.width * scale, height: r.height * scale) }
  ctx.setLineWidth(4); ctx.setStrokeColor(CGColor(red: 0.1, green: 0.9, blue: 0.2, alpha: 0.9))
  for p in ps { ctx.stroke(s(p).insetBy(dx: 3, dy: 3)) }
  ctx.setLineWidth(1); ctx.setStrokeColor(CGColor(red: 0, green: 0.6, blue: 1, alpha: 0.8))
  for l in lines { ctx.stroke(s(l.r)) }
  for (i, b) in bs.enumerated() {
    let r = s(b.r).insetBy(dx: -6, dy: -6)
    ctx.setLineWidth(3); ctx.setStrokeColor(CGColor(red: 1, green: 0, blue: 0.3, alpha: 0.95)); ctx.stroke(r)
    let tag = CGRect(x: r.minX - 2, y: r.minY - 26, width: 30, height: 24)
    ctx.setFillColor(CGColor(red: 1, green: 0, blue: 0.3, alpha: 1)); ctx.fill(tag)
    ctx.saveGState(); ctx.textMatrix = CGAffineTransform(scaleX: 1, y: -1)
    let font = CTFontCreateWithName("Helvetica-Bold" as CFString, 18, nil)
    let str = NSAttributedString(string: "\(i + 1)", attributes: [kCTFontAttributeName as NSAttributedString.Key: font,
      kCTForegroundColorAttributeName as NSAttributedString.Key: CGColor(red: 1, green: 1, blue: 1, alpha: 1)])
    ctx.textPosition = CGPoint(x: tag.minX + 4, y: tag.maxY - 5); CTLineDraw(CTLineCreateWithAttributedString(str), ctx)
    ctx.restoreGState()
  }
  let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.jpeg.identifier as CFString, 1, nil)!
  CGImageDestinationAddImage(dest, ctx.makeImage()!, [kCGImageDestinationLossyCompressionQuality: 0.8] as CFDictionary)
  CGImageDestinationFinalize(dest)
}

let args = CommandLine.arguments
let inDir = URL(fileURLWithPath: args[1]), outDir = URL(fileURLWithPath: args[2]), boxDir = URL(fileURLWithPath: args[3])
let confThr = args.count > 4 ? Double(args[4])! : 0.5
// Host job: skip the debug render, the HTML/TSV and the timing-only fast Vision pass. Output files are unchanged.
let noRender = args.contains("--no-render")
try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)
let files = try FileManager.default.contentsOfDirectory(at: inDir, includingPropertiesForKeys: nil)
  .filter { ["jpg", "jpeg", "png", "webp"].contains($0.pathExtension.lowercased()) }.sorted { $0.path < $1.path }

var html = "<html><head><style>body{font:13px sans-serif;background:#222;color:#ddd}.p{display:inline-block;width:480px;vertical-align:top;margin:8px}img{width:480px}ol{margin:4px 0;padding-left:22px;font-size:11px}</style></head><body><h2>Bubble spike — red = bubble (numbered reading order), blue = Vision text line</h2>"
var jsonl = ""
var tsv = "page\tbubbles\tlines\taccurate_ms\tfast_ms\tfast_bubbles\n"
for f in files {
  guard let img = loadImage(f) else { continue }
  if !noRender { _ = try? detect(img, fast: true) }   // warm-up so first page timing isn't model load
  var t = Date(); let raw = try detect(img, fast: false); let accMs = Int(Date().timeIntervalSince(t) * 1000)
  t = Date(); let rawFast: [Line] = try noRender ? [] : detect(img, fast: true); let fastMs = Int(Date().timeIntervalSince(t) * 1000)
  // drop junk: low confidence, 1-char hits, numbers-only (page numbers)
  let isNum = { (t: String) in t.unicodeScalars.allSatisfy { !CharacterSet.letters.contains($0) } }
  let lines0 = raw.filter { $0.conf >= 0.3 && $0.text.trimmingCharacters(in: .whitespaces).count >= 2 && !isNum($0.text) }
  // stray rules: rotated text (crate labels, signs) and oversized 1-2 word lines (sound effects)
  let hs = lines0.map(\.r.height).sorted(); let medH = hs.isEmpty ? 0 : hs[hs.count / 2]
  // junk glyphs (art read as Cyrillic/odd letters) and labels repeated across a page (crates, notes, signage)
  let latin = { (t: String) -> Double in let ls = t.filter { $0.isLetter }; return ls.isEmpty ? 0 : Double(ls.filter { $0.isASCII }.count) / Double(ls.count) }
  var seen: [String: Int] = [:]
  for l in lines0 { seen[l.text.uppercased().filter { $0.isLetter }, default: 0] += 1 }
  let lines = lines0.filter { l in
    let rotated = l.text.count >= 3 && l.r.height > l.r.width
    let junk = latin(l.text) < 0.8
    let key = l.text.uppercased().filter { $0.isLetter }
    let repeated = key.count >= 4 && seen[key, default: 0] >= 3
    // words without a vowel are art misread as letters ("7HL", "NHTH", "SVAPE SMPO" has one so it stays)
    let words = l.text.uppercased().split(separator: " ").map { $0.filter { $0.isLetter } }.filter { $0.count >= 2 }
    let novowel = !words.isEmpty && words.allSatisfy { !$0.contains { "AEIOUY".contains($0) } }
    return !rotated && !junk && !repeated && !novowel }
  let g = gray(img, width: 1000); let gs = 1000.0 / CGFloat(img.width)
  var boxes: [CGRect] = []
  if let d = try? Data(contentsOf: boxDir.appendingPathComponent(f.deletingPathExtension().lastPathComponent + ".json")),
     let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any], let bl = j["boxes"] as? [[Double]] {
    boxes = bl.filter { $0[4] >= confThr }.map { CGRect(x: $0[0], y: $0[1], width: $0[2] - $0[0], height: $0[3] - $0[1]) }
  }
  let (found, label) = balloons(g, lines, scale: gs, boxes: boxes)
  let outlinedBoxes = found.filter { $0.region > 0 }.map(\.r)
  // panels: a spread is two pages, cut each half on its own; balloons are masked out of the cut
  let isSpread = img.width > Int(Double(img.height) * 1.2)
  var ps: [CGRect] = []
  if isSpread, let l = img.cropping(to: CGRect(x: 0, y: 0, width: img.width / 2, height: img.height)),
     let rt = img.cropping(to: CGRect(x: img.width / 2, y: 0, width: img.width - img.width / 2, height: img.height)) {
    let dx = CGFloat(img.width / 2)
    ps = panels(l, mask: outlinedBoxes) + panels(rt, mask: outlinedBoxes.map { $0.offsetBy(dx: -dx, dy: 0) }).map { $0.offsetBy(dx: dx, dy: 0) }
  } else { ps = panels(img, mask: outlinedBoxes) }
  var bs = orderByPanels(mergeOverlaps(found), ps)
  let outlined = bs.filter { $0.region > 0 }.count
  let gated = bs.isEmpty
  if gated { bs = [] }
  let dropped = lines.count - bs.reduce(0) { $0 + $1.lines.count }
  let fastBs = cluster(rawFast.filter { $0.conf >= 0.3 && $0.text.count >= 2 })
  let name = f.deletingPathExtension().lastPathComponent
  if !noRender { render(img, bs, lines, ps, to: outDir.appendingPathComponent(name + ".jpg")) }
  writeLabels(g, label, bs, to: outDir.appendingPathComponent(name + "_labels.png"))
  let esc = { (s: String) in s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;") }
  html += "<div class=p><b>\(name)</b> — \(bs.count) bubbles (\(dropped) lines dropped), \(ps.count) panels, \(accMs)ms<br><a href='\(name).jpg'><img src='\(name).jpg'></a><ol>"
  html += bs.map { "<li>" + esc($0.lines.sorted { $0.r.minY < $1.r.minY }.map(\.text).joined(separator: " ")) + "</li>" }.joined()
  html += "</ol></div>"
  let rects = bs.map { "[\(Int($0.r.minX)),\(Int($0.r.minY)),\(Int($0.r.width)),\(Int($0.r.height))]" }.joined(separator: ",")
  // each balloon's text, lines top to bottom (a line beside another on the same row: left first)
  let texts = bs.map { b in
    b.lines.sorted { abs($0.r.midY - $1.r.midY) < min($0.r.height, $1.r.height) / 2 ? $0.r.minX < $1.r.minX : $0.r.minY < $1.r.minY }
      .map(\.text).joined(separator: " ")
  }
  let textsJSON = String(data: (try? JSONSerialization.data(withJSONObject: texts)) ?? Data("[]".utf8), encoding: .utf8) ?? "[]"
  jsonl += "{\"page\":\"\(f.lastPathComponent)\",\"w\":\(img.width),\"h\":\(img.height),\"bubbles\":[\(rects)],\"texts\":\(textsJSON)}\n"
  tsv += "\(name)\t\(bs.count)\t\(lines.count)\t\(accMs)\t\(fastMs)\t\(fastBs.count)\n"
  print(name, bs.count, "bubbles", outlined, "outlined", dropped, "dropped", ps.count, "panels", accMs, "ms", gated ? "GATED" : "")
}
if !noRender {
  try (html + "</body></html>").write(to: outDir.appendingPathComponent("index.html"), atomically: true, encoding: .utf8)
  try tsv.write(to: outDir.appendingPathComponent("stats.tsv"), atomically: true, encoding: .utf8)
}

try jsonl.write(to: outDir.appendingPathComponent("bubbles.jsonl"), atomically: true, encoding: .utf8)

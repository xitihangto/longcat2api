// 从本机妙手(CatPaw)客户端取出 LongCat-2.0 通道用的 access token
// —— 纯 Swift + CryptoKit，macOS 自带（/usr/bin/swift），零依赖、无需 node/go。
//
// 用法：
//   swift scripts/import-miaoshou-token.swift          # 打印 token
//   swift scripts/import-miaoshou-token.swift --copy   # 直接复制到剪贴板（推荐）
//   swift scripts/import-miaoshou-token.swift --check  # 顺便联网校验
//
// 原理：客户端把美团 SSO token 用 AES-256-GCM 加密写在
//   ~/Library/Application Support/catpaw-moon/catx-credential.json
// 密钥 = SHA256("<IOPlatformUUID 小写>:catpaw-desk-token-v2")
// 密文布局 = base64( iv[12] || tag[16] || ciphertext )
//
// 拿到后粘进面板「妙手账号」卡片 → 校验并导入。

import Foundation
import CryptoKit

let args = CommandLine.arguments
let doCopy = args.contains("--copy") || args.contains("--clipboard")
let doCheck = args.contains("--check")

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(("✗ " + msg + "\n").data(using: .utf8)!)
    exit(1)
}

let home = FileManager.default.homeDirectoryForCurrentUser.path
let credPath = ProcessInfo.processInfo.environment["CATPAW_CRED"]
    ?? (home + "/Library/Application Support/catpaw-moon/catx-credential.json")

guard FileManager.default.fileExists(atPath: credPath),
      let credData = FileManager.default.contents(atPath: credPath),
      let cred = try? JSONSerialization.jsonObject(with: credData) as? [String: Any],
      let encB64 = cred["ssoTokenEnc"] as? String, !encB64.isEmpty
else {
    fail("找不到凭据或文件里没有 ssoTokenEnc（路径：\(credPath)）\n  确认妙手客户端已登录过。")
}

// 机器码：IOPlatformUUID（小写）
let ioreg = Process()
ioreg.executableURL = URL(fileURLWithPath: "/usr/sbin/ioreg")
ioreg.arguments = ["-rd1", "-c", "IOPlatformExpertDevice"]
let pipe = Pipe()
ioreg.standardOutput = pipe
try? ioreg.run()
ioreg.waitUntilExit()
let out = String(data: pipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
guard let r = out.range(of: "IOPlatformUUID\" = \"") else { fail("未找到 IOPlatformUUID") }
let tail = out[r.upperBound...]
guard let endq = tail.firstIndex(of: "\"") else { fail("IOPlatformUUID 解析失败") }
let machineId = String(tail[..<endq]).lowercased()

// 密钥
let salt = ":catpaw-desk-token-v2"
let keyData = Data(SHA256.hash(data: Data((machineId + salt).utf8)))
let key = SymmetricKey(data: keyData)

// 密文：iv[12] || tag[16] || ciphertext
guard let raw = Data(base64Encoded: encB64), raw.count > 29 else { fail("密文长度异常") }
let iv  = raw.subdata(in: 0..<12)
let tag = raw.subdata(in: 12..<28)
let ct  = raw.subdata(in: 28..<raw.count)
// CryptoKit 的 combined 布局是 nonce || ciphertext || tag
var combined = Data()
combined.append(iv)
combined.append(ct)
combined.append(tag)

let token: String
do {
    let box = try AES.GCM.SealedBox(combined: combined)
    let plain = try AES.GCM.open(box, using: key)
    guard let obj = try JSONSerialization.jsonObject(with: plain) as? [String: Any],
          let t = obj["access_token"] as? String, !t.isEmpty
    else { fail("解密后没有 access_token") }
    token = t
} catch {
    fail("解密失败（\(error)）\n  常见原因：凭据在另一台机器生成（机器码不同），或文件被改动过。")
}

try? token.write(toFile: "/tmp/lc-miaoshou-token.txt", atomically: true, encoding: .utf8)
try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: "/tmp/lc-miaoshou-token.txt")

if doCopy {
    let pbcopy = Process()
    pbcopy.executableURL = URL(fileURLWithPath: "/usr/bin/pbcopy")
    let inPipe = Pipe()
    pbcopy.standardInput = inPipe
    try? pbcopy.run()
    inPipe.fileHandleForWriting.write(token.data(using: .utf8)!)
    try? inPipe.fileHandleForWriting.close()
    pbcopy.waitUntilExit()
    print("✅ 妙手 token 已复制到剪贴板（\(token.count) 字符），直接去面板粘贴即可")
    print("   \(token.prefix(12))…（其余已隐藏）")
} else {
    print("✅ 已取出妙手 token（\(token.count) 字符）：")
    print(token)
}

print("\n已写入 /tmp/lc-miaoshou-token.txt（0600）")
print("下一步：面板「妙手账号」卡片 → 粘贴 → 校验并导入")

if doCheck {
    var req = URLRequest(url: URL(string: "https://catx.nocode.cn/api/gateway/passport/current-user")!)
    req.setValue(token, forHTTPHeaderField: "X-Auth-Token")
    req.setValue("application/json", forHTTPHeaderField: "Accept")
    req.timeoutInterval = 20
    let sem = DispatchSemaphore(value: 0)
    URLSession.shared.dataTask(with: req) { data, _, err in
        defer { sem.signal() }
        if let err = err { print("\n⚠️ 校验失败：\(err.localizedDescription)"); return }
        guard let data = data,
              let j = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            print("\n⚠️ 校验响应异常"); return
        }
        if (j["code"] as? Int) == 0, let d = j["data"] as? [String: Any] {
            print("\n✅ 有效：uid=\(d["userId"] ?? "-") userName=\(d["userName"] ?? "-")")
        } else {
            print("\n❌ 无效：\(String(data: data, encoding: .utf8)?.prefix(120) ?? "")")
        }
    }.resume()
    sem.wait()
}

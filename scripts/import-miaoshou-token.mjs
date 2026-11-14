// 从本机妙手(CatPaw)客户端里取出 LongCat-2.0 通道用的 access token
//
// 用法：
//   node scripts/import-miaoshou-token.mjs            # 取出并打印（同时写到 /tmp/lc-miaoshou-token.txt）
//   node scripts/import-miaoshou-token.mjs --check    # 顺便联网校验 token 是否有效
//
// 原理：客户端把美团 SSO token 用 AES-256-GCM 加密写在
//   macOS:  ~/Library/Application Support/catpaw-moon/catx-credential.json
//   Win:    %APPDATA%\catpaw-moon\catx-credential.json
// 密钥 = sha256("<machineId>:catpaw-desk-token-v2")
//   macOS machineId = IOPlatformUUID（小写）
//   Windows machineId = HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid
// 密文布局 = base64(iv[12] || tag[16] || ciphertext)
//
// 拿到 token 后：粘贴到面板「妙手账号」卡片 → 校验并导入。
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execSync } from 'node:child_process';

const CHECK = process.argv.includes('--check');

function credentialPath() {
  if (process.env.CATPaw_CRED) return process.env.CATPaw_CRED;
  const home = os.homedir();
  if (process.platform === 'darwin') {
    return path.join(home, 'Library/Application Support/catpaw-moon/catx-credential.json');
  }
  if (process.platform === 'win32') {
    return path.join(process.env.APPDATA || '', 'catpaw-moon', 'catx-credential.json');
  }
  return path.join(home, '.config/catpaw-moon/catx-credential.json');
}

function machineID() {
  if (process.platform === 'darwin') {
    const out = execSync('ioreg -rd1 -c IOPlatformExpertDevice').toString();
    const m = out.match(/IOPlatformUUID"\s+=\s+"([^"]+)"/);
    if (!m) throw new Error('未找到 IOPlatformUUID');
    return m[1].trim().toLowerCase();
  }
  if (process.platform === 'win32') {
    const out = execSync('reg QUERY "HKLM\\SOFTWARE\\Microsoft\\Cryptography" /v MachineGuid').toString();
    const m = out.match(/MachineGuid\s+REG_SZ\s+(\S+)/i);
    if (!m) throw new Error('未找到 MachineGuid');
    return m[1].trim();
  }
  throw new Error(`暂不支持 ${process.platform}，请用 -token 手工导入`);
}

function decrypt(mid, encB64) {
  const key = crypto.createHash('sha256').update(`${mid}:catpaw-desk-token-v2`).digest();
  const buf = Buffer.from(encB64, 'base64');
  if (buf.length < 29) throw new Error('密文长度异常');
  const iv = buf.subarray(0, 12);
  const tag = buf.subarray(12, 28);
  const ct = buf.subarray(28);
  const d = crypto.createDecipheriv('aes-256-gcm', key, iv);
  d.setAuthTag(tag);
  const plain = Buffer.concat([d.update(ct), d.final()]).toString('utf8');
  const obj = JSON.parse(plain);
  if (!obj.access_token) throw new Error('解密后没有 access_token');
  return obj.access_token;
}

async function check(token) {
  const res = await fetch('https://catx.nocode.cn/api/gateway/passport/current-user', {
    headers: { 'X-Auth-Token': token, Accept: 'application/json' },
  });
  const txt = await res.text();
  try {
    const j = JSON.parse(txt);
    if (j.code === 0 && j.data) return `✅ 有效：uid=${j.data.userId} userName=${j.data.userName}`;
    return `❌ 无效（code=${j.code}）：${j.message || txt.slice(0, 120)}`;
  } catch {
    return `⚠️ 响应异常 HTTP ${res.status}：${txt.slice(0, 120)}`;
  }
}

const p = credentialPath();
if (!fs.existsSync(p)) {
  console.error(`✗ 找不到凭据文件：${p}\n  确认妙手客户端已登录过；或用 --token 方式从别处导入。`);
  process.exit(1);
}
const cred = JSON.parse(fs.readFileSync(p, 'utf8'));
if (!cred.ssoTokenEnc) {
  console.error('✗ 凭据文件里没有 ssoTokenEnc（客户端未登录？）');
  process.exit(1);
}

const mid = machineID();
const token = decrypt(mid, cred.ssoTokenEnc);
fs.writeFileSync('/tmp/lc-miaoshou-token.txt', token, { mode: 0o600 });

console.log('✅ 已取出妙手 token（长度 ' + token.length + '）：');
console.log(token);
console.log('\n已写入 /tmp/lc-miaoshou-token.txt（0600）');
console.log('下一步：面板「妙手账号」卡片 → 粘贴 → 校验并导入');

if (CHECK) console.log('\n' + (await check(token)));

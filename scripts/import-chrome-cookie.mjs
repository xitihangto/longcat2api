// 从本机 Chrome 读取 longcat 登录态 Cookie（含 HttpOnly），输出到 /tmp/lc-cookie.txt
//
// 用法：
//   node scripts/import-chrome-cookie.mjs
//   # 然后把内容贴进面板「粘贴 Cookie」框，或：
//   curl -X POST http://<网关>/panel/api/channel/login \
//     -H "Authorization: Bearer <api_key>" -H 'Content-Type: application/json' \
//     -d "$(python3 -c 'import json;print(json.dumps({"cookie":open("/tmp/lc-cookie.txt").read()}))')"
//
// 原理：Chrome 的 cookie 用 keychain 里的 "Chrome Safe Storage" 派生密钥
// （PBKDF2-SHA1, salt=saltysalt, 1003 轮, 16 字节）做 AES-128-CBC 加密，v10 前缀。
import crypto from 'node:crypto';
import { execSync } from 'node:child_process';
import fs from 'node:fs';

const DB = '/tmp/ck.db'; // 临时副本，避免直接读运行中的 Chrome 数据库
fs.copyFileSync(process.env.HOME + '/Library/Application Support/Google/Chrome/Default/Cookies', DB);

const pw = execSync(`security find-generic-password -w -s "Chrome Safe Storage" -a Chrome`).toString().trim();
const key = crypto.pbkdf2Sync(pw, 'saltysalt', 1003, 16, 'sha1');
const IV = Buffer.alloc(16, ' ');

function dec(buf) {
  if (!buf || buf.length === 0) return '';
  const s = buf.toString('utf8');
  if (!s.startsWith('v10') && !s.startsWith('v11')) return s; // 明文
  const body = buf.subarray(3);
  const d = crypto.createDecipheriv('aes-128-cbc', key, IV);
  d.setAutoPadding(true);
  let out = Buffer.concat([d.update(body), d.final()]);
  // Chrome DB 版本 >= 24 时，明文前面有 32 字节 domain hash
  if (out.length > 32 && out.subarray(0, 32).some((b) => b < 0x09 || (b > 0x0d && b < 0x20) || b > 0x7e)) {
    out = out.subarray(32);
  }
  return out.toString('utf8');
}

const rows = execSync(`sqlite3 -json ${DB} "select host_key,name,hex(encrypted_value) as enc,value,is_httponly from cookies where host_key like '%longcat%' order by host_key,name;"`).toString();
const list = JSON.parse(rows || '[]');
let cookieParts = [];
for (const r of list) {
  let v = r.value || '';
  if (!v && r.enc) {
    try { v = dec(Buffer.from(r.enc, 'hex')); } catch (e) { console.log(`  ! ${r.name} 解密失败: ${e.message}`); continue; }
  }
  const mask = v.length <= 6 ? v : v.slice(0, 3) + '***' + v.slice(-2);
  console.log(`  ${r.host_key.padEnd(22)} ${r.name.padEnd(42)} httponly=${r.is_httponly} len=${v.length} ${mask}`);
  // 只保留可打印 ASCII 的 cookie（strategy 类是二进制，带上会让 HTTP 头非法）
  if (/^[\x20-\x7e]*$/.test(v) && v.length > 0) cookieParts.push(`${r.name}=${v}`);
  else console.log(`  - 跳过二进制 cookie: ${r.name}`);
}
fs.writeFileSync('/tmp/lc-cookie.txt', cookieParts.join('; '), { mode: 0o600 });
console.log(`\n共 ${list.length} 条，已写入 /tmp/lc-cookie.txt（${fs.statSync('/tmp/lc-cookie.txt').size} bytes）`);

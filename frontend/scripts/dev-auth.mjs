// A dedicated browser profile maps only the application host to local Vite.
// Google and the Node API proxy continue to use normal public DNS and TLS.
import { createServer } from 'vite';
import { existsSync, mkdirSync, readFileSync } from 'node:fs';
import { execFileSync, spawn } from 'node:child_process';
import { X509Certificate, createHash } from 'node:crypto';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

process.chdir(fileURLToPath(new URL('..', import.meta.url)));
const folder = resolve('.local');
mkdirSync(folder, { recursive: true });
const key = resolve(folder, 'dev.key');
const cert = resolve(folder, 'dev.crt');
if (!existsSync(key) || !existsSync(cert)) {
  const openssl = process.env.OPENSSL_PATH || (existsSync('C:/Program Files/Git/usr/bin/openssl.exe') ? 'C:/Program Files/Git/usr/bin/openssl.exe' : 'openssl');
  execFileSync(openssl, ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', key, '-out', cert, '-days', '30', '-subj', '/CN=worth.oopsbox.cn', '-addext', 'subjectAltName=DNS:worth.oopsbox.cn'], { stdio: 'ignore', windowsHide: true });
}
const certificate = new X509Certificate(readFileSync(cert));
if (Date.parse(certificate.validTo) <= Date.now()) throw Error('Local development certificate expired; remove .local/dev.crt and .local/dev.key and run again.');
const pin = createHash('sha256').update(certificate.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
const server = process.argv.includes('--open-only') ? null : await createServer({ server: { host: '127.0.0.1', port: 18101, strictPort: true, allowedHosts: ['worth.oopsbox.cn'], https: { key: readFileSync(key), cert: readFileSync(cert) }, ws: { clientPort: 443 } } });
await server?.listen();
const browser = process.env.EDGE_PATH || ['C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', 'C:/Program Files/Microsoft/Edge/Application/msedge.exe'].find(existsSync);
if (!browser) throw Error('Set EDGE_PATH to the installed Edge executable.');
function systemProxy() {
  if (process.env.DEV_AUTH_PROXY) return process.env.DEV_AUTH_PROXY;
  if (process.platform !== 'win32') return null;
  try {
    const key = 'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings';
    const enabled = execFileSync('reg.exe', ['query', key, '/v', 'ProxyEnable'], { encoding: 'utf8', windowsHide: true });
    if (!/REG_DWORD\s+0x1\b/i.test(enabled)) return null;
    const query = execFileSync('reg.exe', ['query', key, '/v', 'ProxyServer'], { encoding: 'utf8', windowsHide: true });
    const value = query.match(/ProxyServer\s+REG_SZ\s+(.+)/i)?.[1].trim();
    if (!value) return null;
    const fixed = value.includes('=')
      ? value.split(';').find((part) => part.startsWith('http='))?.slice(5) || value.split(';')[0].replace(/^[a-z]+=/i, '')
      : value;
    return /^https?:\/\//i.test(fixed) ? fixed : 'http://' + fixed;
  } catch {
    return null;
  }
}
const proxy = systemProxy();
spawn(browser, [
  `--user-data-dir=${resolve(folder, 'browser')}`, '--no-first-run', '--no-default-browser-check', '--disable-quic',
  '--host-resolver-rules=MAP worth.oopsbox.cn:443 127.0.0.1:18101',
  ...(proxy ? [`--proxy-server=${proxy}`] : []),
  '--proxy-bypass-list=worth.oopsbox.cn',
  `--ignore-certificate-errors-spki-list=${pin}`,
  'https://worth.oopsbox.cn/',
], { detached: true, stdio: 'ignore' }).unref();
console.log('Local frontend: https://worth.oopsbox.cn (dedicated Edge window, local port 18101). API: public backend. Sign in with Google.');
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, async () => { await server?.close(); process.exit(0); });

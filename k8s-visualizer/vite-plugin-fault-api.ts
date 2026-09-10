import type { Plugin } from 'vite';
import { exec, execFile } from 'child_process';
import { promisify } from 'util';
import type { IncomingMessage, ServerResponse } from 'http';

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

interface FaultApiOptions {
  // Optional configuration
}

async function runShell(cmd: string): Promise<{ stdout: string; stderr: string; code: number }> {
  try {
    const { stdout, stderr } = await execAsync(cmd);
    return { stdout: stdout.trim(), stderr: stderr.trim(), code: 0 };
  } catch (err: any) {
    return {
      stdout: err.stdout?.trim() || '',
      stderr: err.stderr?.trim() || err.message,
      code: err.code || 1,
    };
  }
}

async function resolveContainerName(nodeName: string): Promise<string> {
  try {
    const { stdout } = await execFileAsync('docker', ['ps', '--format', '{{.Names}}']);
    const containerNames = stdout.split('\n').map(s => s.trim()).filter(Boolean);

    if (containerNames.includes(nodeName)) {
      return nodeName;
    }

    const match = containerNames.find(name => name.includes(nodeName) || nodeName.includes(name));
    if (match) {
      return match;
    }
  } catch {}

  return nodeName;
}

async function execOnNode(nodeName: string, command: string): Promise<{ stdout: string; stderr: string; code: number }> {
  const container = await resolveContainerName(nodeName);

  // 1. Primary execution strategy: docker exec directly without cmd.exe shell wrapper
  try {
    const { stdout, stderr } = await execFileAsync('docker', ['exec', container, 'sh', '-c', command]);
    return { stdout: stdout.trim(), stderr: stderr.trim(), code: 0 };
  } catch (err: any) {
    // 2. Fallback: minikube ssh
    try {
      const { stdout, stderr } = await execFileAsync('minikube', ['ssh', '-n', nodeName, '--', command]);
      return { stdout: stdout.trim(), stderr: stderr.trim(), code: 0 };
    } catch (mErr: any) {
      return {
        stdout: err.stdout?.trim() || mErr.stdout?.trim() || '',
        stderr: err.stderr?.trim() || mErr.stderr?.trim() || err.message,
        code: err.code || 1,
      };
    }
  }
}

async function resolvePodCaliInterface(nodeName: string, podName: string, namespace: string, podIP?: string): Promise<string> {
  // 1. Direct Calico endpoint-status query on the node (Calico's primary source of truth, works UP or DOWN)
  if (podName) {
    const calicoRes = await execOnNode(nodeName, `grep -o cali[0-9a-f]* /var/run/calico/endpoint-status/*${podName}*`);
    const match = calicoRes.stdout.trim().split(/\s+/)[0];
    if (match && match.startsWith('cali')) {
      return match;
    }
  }

  // 2. Try resolving via host routing table if targetIP is known
  let targetIP = podIP;
  if (!targetIP && podName) {
    try {
      const { stdout } = await execFileAsync('kubectl', ['get', 'pod', podName, '-n', namespace || 'default', '-o', 'jsonpath={.status.podIP}']);
      targetIP = stdout.trim();
    } catch {}
  }

  if (targetIP) {
    const routeRes = await execOnNode(nodeName, `ip route show ${targetIP} | grep -o cali[0-9a-f]*`);
    const match = routeRes.stdout.trim().split(/\s+/)[0];
    if (match && match.startsWith('cali')) {
      return match;
    }
  }

  // 3. Query peer interface index from inside the container via sysfs iflink
  if (podName) {
    try {
      const { stdout } = await execFileAsync('kubectl', ['exec', '-n', namespace || 'default', podName, '--', 'cat', '/sys/class/net/eth0/iflink']);
      const ifindex = stdout.trim().split(/\s+/)[0];
      if (/^\d+$/.test(ifindex)) {
        const linkRes = await execOnNode(nodeName, `ip -o link show | grep "^${ifindex}:" | grep -o cali[0-9a-f]*`);
        const match = linkRes.stdout.trim().split(/\s+/)[0];
        if (match && match.startsWith('cali')) {
          return match;
        }
      }
    } catch {}
  }

  return '';
}

function parseJsonBody<T = any>(req: IncomingMessage): Promise<T> {
  return new Promise((resolve, reject) => {
    let body = '';
    req.on('data', chunk => {
      body += chunk;
    });
    req.on('end', () => {
      try {
        resolve(body ? JSON.parse(body) : {});
      } catch {
        reject(new Error('Invalid JSON body'));
      }
    });
    req.on('error', reject);
  });
}

function sendJson(res: ServerResponse, status: number, data: any) {
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
    'Access-Control-Allow-Headers': 'Content-Type',
  });
  res.end(JSON.stringify(data));
}

export function faultInjectorPlugin(_options: FaultApiOptions = {}): Plugin {
  return {
    name: 'k8s-fault-injector-api',
    configureServer(server) {
      // Middleware runs BEFORE Vite proxy
      server.middlewares.use(async (req, res, next) => {
        const url = req.url || '';

        if (!url.startsWith('/api/fault/')) {
          return next();
        }

        if (req.method === 'OPTIONS') {
          res.writeHead(204, {
            'Access-Control-Allow-Origin': '*',
            'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
            'Access-Control-Allow-Headers': 'Content-Type',
          });
          return res.end();
        }

        try {
          // -------------------------------------------------------------
          // 1. Drop iptables: POST /api/fault/iptables/drop
          // -------------------------------------------------------------
          if (url === '/api/fault/iptables/drop' && req.method === 'POST') {
            const { nodeName } = await parseJsonBody(req);
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            // Check if rule already exists
            const checkRes = await execOnNode(
              nodeName,
              'iptables -C FORWARD -j DROP -m comment --comment "exp11-failure" 2>/dev/null'
            );

            let insertRes = { stdout: '', stderr: '', code: 0 };
            if (checkRes.code !== 0) {
              // Rule doesn't exist yet; insert at top of FORWARD chain
              insertRes = await execOnNode(
                nodeName,
                'iptables -I FORWARD -j DROP -m comment --comment "exp11-failure"'
              );
              if (insertRes.code !== 0) {
                return sendJson(res, 500, {
                  error: `Failed to insert iptables rule: ${insertRes.stderr}`,
                  command: 'iptables -I FORWARD -j DROP -m comment --comment "exp11-failure"',
                });
              }
            }

            // Verify active status
            const verifyRes = await execOnNode(
              nodeName,
              'iptables -C FORWARD -j DROP -m comment --comment "exp11-failure"'
            );

            return sendJson(res, 200, {
              success: verifyRes.code === 0,
              nodeName,
              command: 'iptables -I FORWARD -j DROP -m comment --comment "exp11-failure"',
              message: `Successfully dropped host iptables FORWARD traffic on ${nodeName}`,
              output: insertRes.stdout || 'Rule verified in kernel',
            });
          }

          // -------------------------------------------------------------
          // 2. Restore iptables: POST /api/fault/iptables/restore
          // -------------------------------------------------------------
          if (url === '/api/fault/iptables/restore' && req.method === 'POST') {
            const { nodeName } = await parseJsonBody(req);
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            // Delete rules in a loop until none remain
            let removedCount = 0;
            while (true) {
              const checkRes = await execOnNode(
                nodeName,
                'iptables -C FORWARD -j DROP -m comment --comment "exp11-failure" 2>/dev/null'
              );
              if (checkRes.code !== 0) break; // Rule no longer present

              const delRes = await execOnNode(
                nodeName,
                'iptables -D FORWARD -j DROP -m comment --comment "exp11-failure"'
              );
              if (delRes.code !== 0) break;
              removedCount++;
            }

            return sendJson(res, 200, {
              success: true,
              nodeName,
              command: 'iptables -D FORWARD -j DROP -m comment --comment "exp11-failure"',
              message: `Successfully restored host iptables FORWARD rules on ${nodeName} (removed ${removedCount} rule(s))`,
            });
          }

          // -------------------------------------------------------------
          // 3. Status check: GET /api/fault/iptables/status?nodeName=...
          // -------------------------------------------------------------
          if (url.startsWith('/api/fault/iptables/status') && req.method === 'GET') {
            const parsedUrl = new URL(url, 'http://localhost');
            const nodeName = parsedUrl.searchParams.get('nodeName');
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            const checkRes = await execOnNode(
              nodeName,
              'iptables -C FORWARD -j DROP -m comment --comment "exp11-failure" 2>/dev/null'
            );
            const isDropped = checkRes.code === 0;

            let ruleSummary = '';
            if (isDropped) {
              const listRes = await execOnNode(
                nodeName,
                'iptables -L FORWARD -n -v --line-numbers | grep "exp11-failure"'
              );
              ruleSummary = listRes.stdout;
            }

            return sendJson(res, 200, {
              nodeName,
              dropped: isDropped,
              ruleSummary,
            });
          }

          // -------------------------------------------------------------
          // 4. Real Ping Test: POST /api/fault/ping
          // -------------------------------------------------------------
          if (url === '/api/fault/ping' && req.method === 'POST') {
            const { namespace = 'default', podName, targetIP = '8.8.8.8' } = await parseJsonBody(req);
            if (!podName) {
              return sendJson(res, 400, { error: 'podName is required' });
            }

            const pingCmd = `kubectl exec -n ${namespace} ${podName} -- ping -c 3 -W 1 ${targetIP}`;
            const pingRes = await runShell(pingCmd);

            // Extract packet loss percentage
            const match = pingRes.stdout.match(/(\d+)%\s+packet loss/);
            const packetLoss = match ? parseInt(match[1], 10) : (pingRes.code === 0 ? 0 : 100);

            return sendJson(res, 200, {
              success: pingRes.code === 0 && packetLoss < 100,
              command: `kubectl exec -it ${podName} -n ${namespace} -- ping -c 3 ${targetIP}`,
              output: pingRes.stdout || pingRes.stderr,
              packetLossPercent: packetLoss,
              exitCode: pingRes.code,
            });
          }

          if (url.startsWith('/api/fault/veth/status') && req.method === 'GET') {
            const parsedUrl = new URL(url, 'http://localhost');
            const nodeName = parsedUrl.searchParams.get('nodeName');
            const podName = parsedUrl.searchParams.get('podName') || '';
            const namespace = parsedUrl.searchParams.get('namespace') || 'default';
            const podIP = parsedUrl.searchParams.get('podIP') || '';

            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            const iface = await resolvePodCaliInterface(nodeName, podName, namespace, podIP);
            let isDown = false;
            let statusRaw = '';

            if (iface) {
              const linkCheck = await execOnNode(nodeName, `ip link show ${iface}`);
              statusRaw = linkCheck.stdout;
              isDown = statusRaw.includes('state DOWN');
            }

            return sendJson(res, 200, {
              nodeName,
              podName,
              iface,
              isDown,
              statusRaw,
            });
          }

          if (url === '/api/fault/veth/toggle' && req.method === 'POST') {
            const { nodeName, podName, namespace = 'default', podIP, action = 'down', iface: providedIface } = await parseJsonBody(req);
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            let iface = providedIface;
            if (!iface) {
              iface = await resolvePodCaliInterface(nodeName, podName, namespace, podIP);
            }

            if (!iface) {
              return sendJson(res, 404, { error: `Could not determine Calico veth interface for pod ${podName} on node ${nodeName}` });
            }

            const validAction = action === 'up' ? 'up' : 'down';
            const toggleRes = await execOnNode(nodeName, `sudo ip link set ${iface} ${validAction}`);
            if (toggleRes.code !== 0) {
              return sendJson(res, 500, {
                error: `Failed to set ${iface} ${validAction}: ${toggleRes.stderr}`,
                command: `sudo ip link set ${iface} ${validAction}`,
              });
            }

            const verifyRes = await execOnNode(nodeName, `ip link show ${iface}`);
            const isDown = verifyRes.stdout.includes('state DOWN');

            return sendJson(res, 200, {
              success: true,
              nodeName,
              podName,
              iface,
              action: validAction,
              isDown,
              command: `sudo ip link set ${iface} ${validAction}`,
              message: `Successfully set interface ${iface} on ${nodeName} to ${validAction.toUpperCase()}`,
            });
          }

          if (url.startsWith('/api/fault/tunnel/status') && req.method === 'GET') {
            const parsedUrl = new URL(url, 'http://localhost');
            const nodeName = parsedUrl.searchParams.get('nodeName');
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            const checkRes = await execOnNode(nodeName, 'ip link show tunl0');
            const isDown = checkRes.stdout.includes('state DOWN') || !checkRes.stdout.includes('UP');

            return sendJson(res, 200, {
              nodeName,
              isDown,
              output: checkRes.stdout,
            });
          }

          if (url === '/api/fault/tunnel/toggle' && req.method === 'POST') {
            const { nodeName, action = 'down' } = await parseJsonBody(req);
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            const validAction = action === 'up' ? 'up' : 'down';
            const toggleRes = await execOnNode(nodeName, `ip link set tunl0 ${validAction}`);
            if (toggleRes.code !== 0) {
              return sendJson(res, 500, {
                error: `Failed to set tunl0 ${validAction}: ${toggleRes.stderr}`,
                command: `ip link set tunl0 ${validAction}`,
              });
            }

            const checkRes = await execOnNode(nodeName, 'ip link show tunl0');
            const isDown = checkRes.stdout.includes('state DOWN') || !checkRes.stdout.includes('UP');

            return sendJson(res, 200, {
              success: true,
              nodeName,
              action: validAction,
              isDown,
              command: `ip link set tunl0 ${validAction}`,
              message: `Successfully set tunl0 on ${nodeName} to ${validAction.toUpperCase()}`,
            });
          }

          return sendJson(res, 404, { error: 'Unknown fault API endpoint' });
        } catch (err: any) {
          return sendJson(res, 500, { error: err.message || 'Internal server error' });
        }
      });
    },
  };
}

import type { Plugin } from 'vite';
import { exec } from 'child_process';
import { promisify } from 'util';
import type { IncomingMessage, ServerResponse } from 'http';

const execAsync = promisify(exec);

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
  // If exact node name matches docker container
  const { stdout: psOut } = await runShell(`docker ps --format '{{.Names}}'`);
  const containerNames = psOut.split('\n').map(s => s.trim()).filter(Boolean);

  if (containerNames.includes(nodeName)) {
    return nodeName;
  }

  // Check substring match
  const match = containerNames.find(name => name.includes(nodeName) || nodeName.includes(name));
  if (match) {
    return match;
  }

  return nodeName;
}

async function execOnNode(nodeName: string, command: string): Promise<{ stdout: string; stderr: string; code: number }> {
  const container = await resolveContainerName(nodeName);

  // 1. Primary execution strategy: docker exec
  const dockerCmd = `docker exec ${container} sh -c "${command.replace(/"/g, '\\"')}"`;
  const dockerRes = await runShell(dockerCmd);
  if (dockerRes.code === 0) {
    return dockerRes;
  }

  // If docker failed with no such container or permission, try minikube ssh
  if (dockerRes.stderr.includes('No such container') || dockerRes.stderr.includes('permission denied')) {
    const minikubeCmd = `minikube ssh -n ${nodeName} -- "${command.replace(/"/g, '\\"')}"`;
    const minikubeRes = await runShell(minikubeCmd);
    if (minikubeRes.code === 0) {
      return minikubeRes;
    }
  }

  return dockerRes;
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

          return sendJson(res, 404, { error: 'Unknown fault API endpoint' });
        } catch (err: any) {
          return sendJson(res, 500, { error: err.message || 'Internal server error' });
        }
      });
    },
  };
}

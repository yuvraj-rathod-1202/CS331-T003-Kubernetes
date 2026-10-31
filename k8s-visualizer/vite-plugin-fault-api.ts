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

const CR_NAME = 'default-resilience';
const SAFE_NAME = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
const RECOVERY_TAINT = 'cniresilience.cn-operator/recovering:NoSchedule';
const FAULT_TAG = 'cniresilience-drop';
const POD_DROP_RULE = `FORWARD -s 10.244.0.0/16 -j DROP -m comment --comment "${FAULT_TAG}"`;
// Older demo versions also inserted this; the lab uses IPIP, so it never matched a packet.
const LEGACY_VXLAN_RULE = `INPUT -p udp --dport 4789 -j DROP -m comment --comment "${FAULT_TAG}"`;

async function kubectlJson(args: string[]): Promise<{ ok: true; data: any } | { ok: false; error: string }> {
  try {
    const { stdout } = await execFileAsync('kubectl', args, { timeout: 8000, maxBuffer: 20 * 1024 * 1024 });
    return { ok: true, data: JSON.parse(stdout) };
  } catch (err: any) {
    return { ok: false, error: err.stderr?.trim() || err.message };
  }
}

// Number of demo rules in the node's kernel, or null if the node could not be read.
async function countFaultRules(nodeName: string): Promise<number | null> {
  const { stdout } = await execOnNode(nodeName, `iptables-save | grep -c ${FAULT_TAG} || true`);
  const count = parseInt(stdout, 10);
  return Number.isNaN(count) ? null : count;
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

            const ruleCheck = await execOnNode(nodeName, 'iptables -C OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop" 2>/dev/null');
            const linkCheck = await execOnNode(nodeName, 'ip link show tunl0');
            const isDown = ruleCheck.code === 0 || linkCheck.stdout.includes('state DOWN') || !linkCheck.stdout.includes('UP');

            return sendJson(res, 200, {
              nodeName,
              isDown,
              output: linkCheck.stdout || (ruleCheck.code === 0 ? 'IPIP encapsulation DROPPED via iptables' : ''),
            });
          }

          if (url === '/api/fault/tunnel/toggle' && req.method === 'POST') {
            const { nodeName, action = 'down' } = await parseJsonBody(req);
            if (!nodeName) {
              return sendJson(res, 400, { error: 'nodeName is required' });
            }

            const validAction = action === 'up' ? 'up' : 'down';
            let commandRan = '';

            if (validAction === 'down') {
              // Ensure iptables rule blocking IPIP protocol (protocol 4) is present
              const ruleCheck = await execOnNode(nodeName, 'iptables -C OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop" 2>/dev/null');
              if (ruleCheck.code !== 0) {
                await execOnNode(nodeName, 'iptables -I OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop"');
              }
              await execOnNode(nodeName, 'ip link set tunl0 down 2>/dev/null || true');
              commandRan = 'iptables -I OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop" && ip link set tunl0 down';
            } else {
              // Clean up iptables rule and bring interface up
              while (true) {
                const check = await execOnNode(nodeName, 'iptables -C OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop" 2>/dev/null');
                if (check.code !== 0) break;
                await execOnNode(nodeName, 'iptables -D OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop"');
              }
              await execOnNode(nodeName, 'ip link set tunl0 up 2>/dev/null || true');
              commandRan = 'iptables -D OUTPUT -p 4 -j DROP && ip link set tunl0 up';
            }

            const verifyRule = await execOnNode(nodeName, 'iptables -C OUTPUT -p 4 -j DROP -m comment --comment "tunl0-drop" 2>/dev/null');
            const verifyLink = await execOnNode(nodeName, 'ip link show tunl0');
            const isDown = verifyRule.code === 0 || verifyLink.stdout.includes('state DOWN') || !verifyLink.stdout.includes('UP');

            return sendJson(res, 200, {
              success: true,
              nodeName,
              action: validAction,
              isDown,
              command: commandRan,
              message: validAction === 'down' 
                ? `Successfully blocked IPIP tunnel traffic (OUTPUT -p 4 DROP) and downed tunl0 on ${nodeName}`
                : `Successfully unblocked IPIP tunnel traffic and brought tunl0 UP on ${nodeName}`,
            });
          }

          // -------------------------------------------------------------
          // 5. CNI Resilience Status: GET /api/fault/cniresilience/status
          // -------------------------------------------------------------
          if (url.startsWith('/api/fault/cniresilience/status') && req.method === 'GET') {
            const [nodes, cr, pods, calicoPods, slices, nad] = await Promise.all([
              kubectlJson(['get', 'nodes', '-o', 'json']),
              kubectlJson(['get', 'cniresilience', CR_NAME, '-o', 'json']),
              kubectlJson(['get', 'pods', '-n', 'default', '-l', 'app=resilience-target', '-o', 'json']),
              kubectlJson(['get', 'pods', '-n', 'kube-system', '-l', 'k8s-app=calico-node', '-o', 'json']),
              kubectlJson(['get', 'endpointslices', '-n', 'default', '-l', 'kubernetes.io/service-name=resilience-service', '-o', 'json']),
              kubectlJson(['get', 'network-attachment-definitions.k8s.cni.cncf.io', 'flannel-backup', '-n', 'kube-system', '-o', 'json']),
            ]);

            if (!nodes.ok) {
              return sendJson(res, 200, { success: false, error: nodes.error });
            }

            const nodeNames: string[] = nodes.data.items.map((n: any) => n.metadata.name);
            const faultCounts = await Promise.all(nodeNames.map(countFaultRules));

            return sendJson(res, 200, {
              success: true,
              nodes: nodes.data.items,
              faultRules: Object.fromEntries(nodeNames.map((name, i) => [name, faultCounts[i]])),
              cr: cr.ok ? cr.data : null,
              pods: pods.ok ? pods.data.items : [],
              calicoPods: calicoPods.ok ? calicoPods.data.items : [],
              endpointSlices: slices.ok ? slices.data.items : [],
              nadConfig: nad.ok ? nad.data.spec?.config ?? null : null,
            });
          }

          // -------------------------------------------------------------
          // 6. Real traffic samples: GET /api/fault/cniresilience/traffic
          //    Reads what the in-cluster traffic-client pod actually measured.
          // -------------------------------------------------------------
          if (url.startsWith('/api/fault/cniresilience/traffic') && req.method === 'GET') {
            try {
              const { stdout } = await execFileAsync(
                'kubectl',
                ['logs', 'traffic-client', '-n', 'default', '--tail=60'],
                { timeout: 8000 }
              );
              const samples = stdout.split('\n').flatMap(line => {
                const match = line.trim().match(/^(\d+) (\d{3}) ([\d.]+)$/);
                return match
                  ? [{ epoch: Number(match[1]), httpCode: match[2], seconds: Number(match[3]) }]
                  : [];
              });
              return sendJson(res, 200, { deployed: true, samples });
            } catch (err: any) {
              return sendJson(res, 200, { deployed: false, samples: [], error: err.stderr?.trim() || err.message });
            }
          }

          // -------------------------------------------------------------
          // 7. CNI Resilience Break: POST /api/fault/cniresilience/break
          // -------------------------------------------------------------
          if (url === '/api/fault/cniresilience/break' && req.method === 'POST') {
            const { nodeName, method } = await parseJsonBody(req);
            if (!nodeName || !SAFE_NAME.test(nodeName)) {
              return sendJson(res, 400, { success: false, error: 'A valid nodeName is required' });
            }

            if (method === 'calico-agent') {
              const args = ['delete', 'pod', '-n', 'kube-system', '-l', 'k8s-app=calico-node',
                '--field-selector', `spec.nodeName=${nodeName}`, '--now'];
              const command = `kubectl ${args.join(' ')}`;
              try {
                const { stdout } = await execFileAsync('kubectl', args, { timeout: 30000 });
                const deleted = stdout.includes('deleted');
                return sendJson(res, deleted ? 200 : 500, {
                  success: deleted,
                  command,
                  message: stdout.trim() || `No calico-node pod found on ${nodeName}`,
                });
              } catch (err: any) {
                return sendJson(res, 500, { success: false, command, error: err.stderr?.trim() || err.message });
              }
            }

            if (method === 'pod-traffic') {
              const command = `iptables -I ${POD_DROP_RULE}`;
              const injected = await execOnNode(nodeName, `iptables -C ${POD_DROP_RULE} 2>/dev/null || ${command}`);
              const present = await countFaultRules(nodeName);
              if (injected.code !== 0 || !present) {
                return sendJson(res, 500, {
                  success: false,
                  command,
                  error: injected.stderr || `Rule was not found on ${nodeName} after inserting it`,
                });
              }
              return sendJson(res, 200, { success: true, command, message: `Rule active on ${nodeName}` });
            }

            return sendJson(res, 400, { success: false, error: `Unknown method: ${method}` });
          }

          // -------------------------------------------------------------
          // 8. CNI Resilience Reset: POST /api/fault/cniresilience/restore
          //    Clears demo faults on every node, plus what the operator left.
          // -------------------------------------------------------------
          if (url === '/api/fault/cniresilience/restore' && req.method === 'POST') {
            const nodes = await kubectlJson(['get', 'nodes', '-o', 'json']);
            if (!nodes.ok) {
              return sendJson(res, 500, { success: false, error: nodes.error });
            }
            const nodeNames: string[] = nodes.data.items.map((n: any) => n.metadata.name);

            const remaining = await Promise.all(nodeNames.map(async name => {
              await execOnNode(
                name,
                `while iptables -D ${POD_DROP_RULE} 2>/dev/null; do :; done; ` +
                `while iptables -D ${LEGACY_VXLAN_RULE} 2>/dev/null; do :; done; true`
              );
              await runShell(`kubectl taint nodes ${name} ${RECOVERY_TAINT}-`);
              return countFaultRules(name);
            }));
            const shadows = await runShell(
              'kubectl delete endpointslices -A -l cniresilience.cn-operator/shadow=true'
            );

            const clean = remaining.every(count => count === 0) && shadows.code === 0;
            return sendJson(res, clean ? 200 : 500, {
              success: clean,
              command: 'iptables -D (demo rules), kubectl taint (recovery taint), kubectl delete endpointslices (shadows)',
              message: `Rules left per node: ${nodeNames.map((n, i) => `${n}=${remaining[i]}`).join(', ')}. ${shadows.stdout}`,
              error: clean ? undefined : shadows.stderr || 'Some rules could not be removed',
            });
          }

          // -------------------------------------------------------------
          // 9. Operator settings: POST /api/fault/cniresilience/config
          // -------------------------------------------------------------
          if (url === '/api/fault/cniresilience/config' && req.method === 'POST') {
            const { autoFailover, autoRecover } = await parseJsonBody(req);
            const spec: Record<string, boolean> = {};
            if (typeof autoFailover === 'boolean') spec.autoFailover = autoFailover;
            if (typeof autoRecover === 'boolean') spec.autoRecover = autoRecover;
            if (Object.keys(spec).length === 0) {
              return sendJson(res, 400, { success: false, error: 'autoFailover or autoRecover is required' });
            }

            const patch = JSON.stringify({ spec });
            try {
              await execFileAsync('kubectl', ['patch', 'cniresilience', CR_NAME, '--type=merge', '-p', patch], { timeout: 8000 });
              return sendJson(res, 200, { success: true, command: `kubectl patch cniresilience ${CR_NAME} --type=merge -p '${patch}'` });
            } catch (err: any) {
              return sendJson(res, 500, { success: false, error: err.stderr?.trim() || err.message });
            }
          }

          return sendJson(res, 404, { error: 'Unknown fault API endpoint' });
        } catch (err: any) {
          return sendJson(res, 500, { error: err.message || 'Internal server error' });
        }
      });
    },
  };
}

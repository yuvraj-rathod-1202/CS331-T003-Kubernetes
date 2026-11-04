import type { Plugin } from 'vite';
import { exec, execFile } from 'child_process';
import { promisify } from 'util';
import type { IncomingMessage, ServerResponse } from 'http';
import { nodeVerdicts, type Links } from './path-verdict.js';

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

function isNodeName(v: unknown): v is string {
  return typeof v === 'string' && /^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$/.test(v);
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
// Same rule as the page: no reconcile for 30 s means the operator is not running.
const OPERATOR_STALE_MS = 30_000;
const SAFE_NAME = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
const RECOVERY_TAINT = 'cniresilience.cn-operator/recovering:NoSchedule';
const FAULT_TAG = 'cniresilience-drop';
const POD_DROP_RULE = `FORWARD -s 172.16.0.0/16 -j DROP -m comment --comment "${FAULT_TAG}"`;
// Deletes every rule tagged FAULT_TAG in every table: this page's rules, rules from
// older page versions, and hand-made test rules such as raw-table link drops.
// iptables -S prints each rule in a form that, with -A turned into -D, deletes it.
const REMOVE_TAGGED_RULES =
  `for t in filter nat mangle raw; do iptables -t $t -S | grep -- '--comment ${FAULT_TAG}' | ` +
  `sed 's/^-A /-D /' | while read -r rule; do eval iptables -t $t $rule; done; done; true`;

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

// Every node runs a path-probe pod. Each one sends a real request to all the others,
// so a failure can be blamed on the right node instead of on one fixed observer.
async function probeNodePaths(nodeNames: string[]) {
  const measuredAt = Date.now();
  const probes = await kubectlJson(['get', 'pods', '-n', 'default', '-l', 'app=path-probe', '-o', 'json']);
  if (!probes.ok) {
    return { measuredAt, nodes: nodeVerdicts(nodeNames, {}), error: probes.error };
  }

  const running: any[] = probes.data.items.filter((p: any) => p.status.phase === 'Running' && p.status.podIP);
  const links: Links = {};
  const failures: string[] = [];

  await Promise.all(running.map(async source => {
    const requests = running
      .filter(target => target.spec.nodeName !== source.spec.nodeName)
      .map(target => `(echo "${target.spec.nodeName} $(curl -s -m 1 -o /dev/null -w '%{http_code}' http://${target.status.podIP})") &`)
      .join(' ');
    try {
      const { stdout } = await execFileAsync(
        'kubectl',
        ['exec', source.metadata.name, '-n', 'default', '--', 'sh', '-c', `${requests} wait`],
        { timeout: 6000 }
      );
      links[source.spec.nodeName] = {};
      for (const line of stdout.split('\n')) {
        const match = line.trim().match(/^(\S+) (\d{3})$/);
        if (match) links[source.spec.nodeName][match[1]] = match[2] === '200';
      }
    } catch (err: any) {
      failures.push(`${source.metadata.name}: ${err.stderr?.trim() || err.message}`);
    }
  }));

  return {
    measuredAt,
    nodes: nodeVerdicts(nodeNames, links),
    error: running.length === 0 ? 'no path-probe pods are running' : failures.join('; ') || undefined,
  };
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
            const [faultCounts, paths] = await Promise.all([
              Promise.all(nodeNames.map(countFaultRules)),
              probeNodePaths(nodeNames),
            ]);

            // A failed read is reported, so it never looks like "nothing there".
            const absent = /NotFound|doesn't have a resource type/;
            const sources: [string, typeof pods][] = [
              ['workload pods', pods], ['calico-node pods', calicoPods], ['EndpointSlices', slices],
              ['CNIResilience CR', cr], ['backup network', nad],
            ];
            const sourceErrors = sources.flatMap(([name, source]) =>
              !source.ok && !absent.test(source.error) ? [`Could not read ${name}: ${source.error}`] : []);

            return sendJson(res, 200, {
              success: true,
              nodes: nodes.data.items,
              faultRules: Object.fromEntries(nodeNames.map((name, i) => [name, faultCounts[i]])),
              cr: cr.ok ? cr.data : null,
              pods: pods.ok ? pods.data.items : [],
              calicoPods: calicoPods.ok ? calicoPods.data.items : [],
              endpointSlices: slices.ok ? slices.data.items : [],
              nadConfig: nad.ok ? nad.data.spec?.config ?? null : null,
              paths,
              sourceErrors,
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

            if (method === 'ipip-tunnel') {
              // Only the interface goes down. Deliberately NO iptables rule: calico-node
              // reprograms tunl0 when it starts, so restarting the agent -- which is the
              // operator's own repair action -- really does fix this fault. Adding a rule
              // here (as the ControlPanel tunnel toggle does) would survive the restart
              // and turn a full self-heal into a half-fix.
              const command = 'ip link set tunl0 down';
              const down = await execOnNode(nodeName, command);
              if (down.code !== 0) {
                return sendJson(res, 500, { success: false, command, error: down.stderr || 'Could not take tunl0 down' });
              }
              // Do NOT fail when tunl0 is already back up. calico-node supervises
              // allocate-tunnel-addrs alongside felix, and that re-ups the interface
              // within about a second, often before the probe threshold is crossed.
              // The command succeeded either way; report what the interface did.
              const state = await execOnNode(nodeName, "ip -br link show tunl0");
              const backUp = / UP[ ,]|,UP[,>]/.test(state.stdout);
              return sendJson(res, 200, {
                success: true,
                command,
                message: backUp
                  ? `tunl0 was taken down on ${nodeName}; Calico had already restored it when checked. If the outage was shorter than 3 probes, the operator will not react.`
                  : `tunl0 is down on ${nodeName}. Watch for failover, then Calico restoring the interface.`,
              });
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

            // While the operator runs, it undoes its own taint and shadows once its probes
            // see the path working. Deleting them first makes it see "broken, not failed
            // over" and fail over and restart the CNI agent again (seen in the lab).
            const cr = await kubectlJson(['get', 'cniresilience', CR_NAME, '-o', 'json']);
            const last = cr.ok ? cr.data.status?.lastReconcileTime : undefined;
            const operatorRunning = !!last && Date.now() - new Date(last).getTime() <= OPERATOR_STALE_MS;

            const remaining = await Promise.all(nodeNames.map(async name => {
              await execOnNode(name, REMOVE_TAGGED_RULES);
              // Bring tunl0 back up in case the IPIP fault was injected and the operator
              // was not running to repair it. A no-op when the tunnel is already up.
              await execOnNode(name, 'ip link set tunl0 up 2>/dev/null || true');
              if (!operatorRunning) await runShell(`kubectl taint nodes ${name} ${RECOVERY_TAINT}-`);
              return countFaultRules(name);
            }));
            const shadows = operatorRunning
              ? { code: 0, stdout: 'Operator is running, so it removes its own taints and shadow slices.', stderr: '' }
              : await runShell('kubectl delete endpointslices -A -l cniresilience.cn-operator/shadow=true');

            const clean = remaining.every(count => count === 0) && shadows.code === 0;
            return sendJson(res, clean ? 200 : 500, {
              success: clean,
              command: operatorRunning
                ? 'iptables -D (demo rules)'
                : 'iptables -D (demo rules), kubectl taint (recovery taint), kubectl delete endpointslices (shadows)',
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

          // -------------------------------------------------------------
          // 9. IPAM (Problem 1) workload helpers: POST /api/fault/ipam/*
          //    These create real load on the cluster the IPAM operator manages.
          // -------------------------------------------------------------
          if (url.startsWith('/api/fault/ipam/status') && req.method === 'GET') {
            // Everything the Problem 1 page shows, read with kubectl (like the Problem 2 status),
            // so the page needs no kubectl proxy; only the same KUBECONFIG as the buttons.
            const [nodes, blocks, pools, pods, poolEvents, sandboxEvents, agent, agentPods, agentLog] = await Promise.all([
              kubectlJson(['get', 'nodes', '-o', 'json']),
              kubectlJson(['get', 'nodeblocks', '-o', 'json']),
              kubectlJson(['get', 'ippools', '-o', 'json']),
              kubectlJson(['get', 'pods', '-A', '-o', 'json']),
              // Only the two kinds of event the page shows; all events would be thousands with 150 pods.
              kubectlJson(['get', 'events', '-A', '--field-selector', 'involvedObject.kind=IPPool', '-o', 'json']),
              kubectlJson(['get', 'events', '-A', '--field-selector', 'reason=FailedCreatePodSandBox', '-o', 'json']),
              kubectlJson(['get', 'daemonset', 'ipam-agent', '-n', 'kube-system', '-o', 'json']),
              kubectlJson(['get', 'pods', '-n', 'kube-system', '-l', 'app=ipam-agent', '-o', 'json']),
              runShell('kubectl logs -n kube-system -l app=ipam-agent --since=30s --prefix --tail=60 --max-log-requests=20'),
            ]);
            if (!nodes.ok) return sendJson(res, 200, { success: false, error: nodes.error });
            const absent = /NotFound|doesn't have a resource type|the server could not find/;
            const eventRows = [
              ...(poolEvents.ok ? poolEvents.data.items as any[] : []),
              ...(sandboxEvents.ok ? (sandboxEvents.data.items as any[]).filter(e => String(e.message).includes('no IP addresses available')) : []),
            ];
            const podRows = pods.ok ? (pods.data.items as any[])
              .filter(p => !p.spec?.hostNetwork && p.status?.podIP)
              .map(p => ({
                name: p.metadata.name, ns: p.metadata.namespace, node: p.spec.nodeName ?? '', ip: p.status.podIP, phase: p.status.phase,
                app: p.metadata.labels?.app ?? '', terminating: !!p.metadata.deletionTimestamp,
              })) : [];
            // The agents' own log lines, tagged with the node each pod runs on.
            const agentNode: Record<string, string> = {};
            if (agentPods.ok) for (const p of agentPods.data.items as any[]) agentNode[p.metadata.name] = p.spec.nodeName;
            const agentLines = (agentLog.stdout || '').split('\n').flatMap(line => {
              const m = line.match(/^\[pod\/([^/]+)\/[^\]]+\] (.*)$/);
              return m ? [{ node: agentNode[m[1]] ?? m[1], line: m[2] }] : [];
            });
            const ds = agent.ok ? agent.data.status : null;
            return sendJson(res, 200, {
              success: true,
              nodes: nodes.data.items,
              blocks: blocks.ok ? blocks.data.items : [],
              pools: pools.ok ? pools.data.items : [],
              crdMissing: (!pools.ok && absent.test(pools.error)) || (!blocks.ok && absent.test(blocks.error)),
              pods: podRows,
              events: eventRows,
              agentLog: agentLines,
              agents: ds ? { desired: ds.desiredNumberScheduled ?? 0, ready: ds.numberReady ?? 0 } : null,
              sourceErrors: [pools, blocks, pods, poolEvents].flatMap(r => (!r.ok && !absent.test(r.error) ? [r.error] : [])),
            });
          }

          if (url === '/api/fault/ipam/scale' && req.method === 'POST') {
            // The demo workload: a Deployment of real pods (pause containers). Scaling it up makes the
            // scheduler place real pods on the nodes; each one takes a real address from its node's block.
            const { replicas, nodeName } = await parseJsonBody(req);
            const n = Number(replicas);
            if (!Number.isInteger(n) || n < 0 || n > 300) return sendJson(res, 400, { error: 'replicas must be an integer 0..300' });
            if (nodeName !== undefined && !isNodeName(nodeName)) return sendJson(res, 400, { error: 'invalid nodeName' });
            const name = nodeName ? `web-${String(nodeName).toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 60) : 'web';
            const manifest = {
              apiVersion: 'apps/v1', kind: 'Deployment',
              metadata: { name, namespace: 'default', labels: { app: 'ipam-demo' } },
              spec: {
                replicas: n,
                selector: { matchLabels: { app: 'ipam-demo', workload: name } },
                template: {
                  metadata: { labels: { app: 'ipam-demo', workload: name } },
                  spec: {
                    ...(nodeName ? { nodeSelector: { 'kubernetes.io/hostname': nodeName } } : {}),
                    topologySpreadConstraints: nodeName ? undefined : [{
                      maxSkew: 1, topologyKey: 'kubernetes.io/hostname', whenUnsatisfiable: 'ScheduleAnyway',
                      labelSelector: { matchLabels: { app: 'ipam-demo', workload: name } },
                    }],
                    terminationGracePeriodSeconds: 0,
                    containers: [{ name: 'pause', image: 'registry.k8s.io/pause:3.10', resources: { requests: { cpu: '1m', memory: '1Mi' } } }],
                  },
                },
              },
            };
            const p = execFileAsync('kubectl', ['apply', '-f', '-']);
            p.child.stdin?.end(JSON.stringify(manifest));
            const cmd = `kubectl apply -f - (Deployment ${name}, replicas=${n}${nodeName ? `, only on ${nodeName}` : ', spread across all nodes'})`;
            try {
              const { stdout } = await p;
              return sendJson(res, 200, { success: true, command: cmd, output: stdout.trim() });
            } catch (e: any) {
              return sendJson(res, 500, { success: false, command: cmd, error: e.stderr?.trim() || e.message });
            }
          }

          if (url === '/api/fault/ipam/clear-load' && req.method === 'POST') {
            const r = await runShell('kubectl delete deployment -l app=ipam-demo --ignore-not-found');
            return sendJson(res, 200, { success: r.code === 0, command: 'kubectl delete deployment -l app=ipam-demo', output: r.stdout || r.stderr });
          }

          return sendJson(res, 404, { error: 'Unknown fault API endpoint' });
        } catch (err: any) {
          return sendJson(res, 500, { error: err.message || 'Internal server error' });
        }
      });
    },
  };
}

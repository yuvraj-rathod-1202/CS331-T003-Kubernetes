import { useCallback, useEffect, useRef, useState } from 'react';
import { Boxes, RefreshCw, Trash2, ScrollText, BarChart3, BookOpen, Rocket, FlaskConical } from 'lucide-react';
import { aggregate, usable } from '../ipam/engine';
import { policies, scenarios, sweep, liveChecks } from '../ipam/evalData';
import './Project1Demo.css';

// Live view of the buddy IPAM operator. Everything on the left and in the node cards is read from
// the Kubernetes API (via `kubectl proxy`, like the other pages); the buttons create real pods or
// real lease files on the cluster. Nothing here is simulated. The offline simulator is at /project1/sim.

const GROUP = '/apis/remediation.cn-operator.yuvraj-rathod-1202.github.io/v1alpha1';
const NODE_COLORS = ['#3b82f6', '#f97316', '#10b981', '#e879a9', '#8b5cf6', '#eab308', '#14b8a6', '#ef4444'];
const POLICY_COLORS = ['#3b82f6', '#f97316', '#10b981', '#e879a9'];

interface Blk { name: string; node: string; cidr: string; phase: string; drain: boolean; used: number; acked: string[]; reclaimed: number; base: number; size: number }
interface NodeInfo { name: string; ready: boolean; ip: string; maxPods: number }
interface PodInfo { name: string; ns: string; node: string; ip: string; phase: string }
interface Pool { name: string; spec: any; status: any }
interface EventRow { time: number; kind: 'controller' | 'kubelet'; reason: string; message: string }
interface ActionResult { ok: boolean; command: string; output: string; at: number }

const ipToNum = (ip: string) => ip.split('.').reduce((a, o) => ((a << 8) | Number(o)) >>> 0, 0) >>> 0;
const numToIp = (n: number) => [n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join('.');
const parseCidr = (c: string) => { const [ip, bits] = c.split('/'); return { base: ipToNum(ip), size: 2 ** (32 - Number(bits)) }; };

async function getJson(path: string) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
  return r.json();
}

export function Project1Demo() {
  const [conn, setConn] = useState<'connecting' | 'ok' | 'error'>('connecting');
  const [err, setErr] = useState('');
  const [nodes, setNodes] = useState<NodeInfo[]>([]);
  const [blocks, setBlocks] = useState<Blk[]>([]);
  const [pods, setPods] = useState<PodInfo[]>([]);
  const [pool, setPool] = useState<Pool | null>(null);
  const [events, setEvents] = useState<EventRow[]>([]);
  const [agents, setAgents] = useState({ ready: 0, total: 0 });
  const [tab, setTab] = useState<'log' | 'eval' | 'how'>('log');
  const [hover, setHover] = useState<string | null>(null);
  const [last, setLast] = useState<ActionResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [podsWanted, setPodsWanted] = useState<Record<string, number>>({});
  const [leases, setLeases] = useState(150);
  const [now, setNow] = useState(Date.now());
  const alive = useRef(true);

  const refresh = useCallback(async () => {
    try {
      const [nd, bl, pl, pd, ev, ag] = await Promise.all([
        getJson('/api/v1/nodes'),
        getJson(`${GROUP}/nodeblocks`),
        getJson(`${GROUP}/ippools`),
        getJson('/api/v1/pods'),
        getJson('/api/v1/events').catch(() => ({ items: [] })),
        getJson('/api/v1/namespaces/kube-system/pods?labelSelector=app%3Dipam-agent').catch(() => ({ items: [] })),
      ]);
      if (!alive.current) return;
      setNodes(nd.items.map((n: any) => ({
        name: n.metadata.name,
        ready: n.status.conditions?.find((c: any) => c.type === 'Ready')?.status === 'True',
        ip: n.status.addresses?.find((a: any) => a.type === 'InternalIP')?.address ?? '',
        maxPods: Number(n.status.allocatable?.pods ?? 110),
      })).sort((a: NodeInfo, b: NodeInfo) => a.name.localeCompare(b.name)));
      setBlocks(bl.items.map((b: any) => {
        const c = parseCidr(b.spec.cidr);
        return {
          name: b.metadata.name, node: b.spec.nodeName, cidr: b.spec.cidr, phase: b.status?.phase || 'Pending',
          drain: !!b.spec.drain, used: b.status?.usedAddresses ?? 0, acked: b.status?.routesAckedBy ?? [],
          reclaimed: b.status?.reclaimedLeases ?? 0, base: c.base, size: c.size,
        } as Blk;
      }));
      const pools = [...pl.items].sort((a: any, b: any) => String(a.metadata.creationTimestamp).localeCompare(String(b.metadata.creationTimestamp)));
      setPool(pools[0] ? { name: pools[0].metadata.name, spec: pools[0].spec, status: pools[0].status ?? {} } : null);
      setPods(pd.items.filter((p: any) => !p.spec.hostNetwork && p.status?.podIP).map((p: any) => ({
        name: p.metadata.name, ns: p.metadata.namespace, node: p.spec.nodeName ?? '', ip: p.status.podIP, phase: p.status.phase,
      })));
      const rows: EventRow[] = [];
      for (const e of ev.items) {
        const t = Date.parse(e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp);
        if (e.involvedObject?.kind === 'IPPool') rows.push({ time: t, kind: 'controller', reason: e.reason, message: e.message });
        else if (e.reason === 'FailedCreatePodSandBox' && String(e.message).includes('no IP addresses available'))
          rows.push({ time: t, kind: 'kubelet', reason: e.reason, message: `${e.source?.host ?? ''}: no IP addresses available in range set` });
      }
      setEvents(rows.sort((a, b) => a.time - b.time).slice(-60));
      setAgents({ total: ag.items.length, ready: ag.items.filter((p: any) => p.status?.containerStatuses?.every((c: any) => c.ready)).length });
      setConn('ok');
      setNow(Date.now());
    } catch (e: any) {
      if (!alive.current) return;
      setConn('error');
      setErr(e.message);
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    refresh();
    const id = setInterval(refresh, 2000);
    return () => { alive.current = false; clearInterval(id); };
  }, [refresh]);

  const act = async (path: string, body: object) => {
    setBusy(true);
    try {
      const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const j = await r.json();
      setLast({ ok: r.ok && j.success !== false, command: j.command ?? path, output: j.output ?? j.error ?? '', at: Date.now() });
    } catch (e: any) {
      setLast({ ok: false, command: path, output: e.message, at: Date.now() });
    }
    setBusy(false);
    refresh();
  };

  const colorOf = (node: string) => NODE_COLORS[Math.max(0, nodes.findIndex(n => n.name === node)) % NODE_COLORS.length];
  const podsOn = (node: string) => pods.filter(p => p.node === node && p.phase === 'Running').length;
  const cidrs: string[] = pool?.spec?.cidrs ?? [];
  const stats = pool?.status ?? {};
  const totalBlocks = blocks.length;
  const pending = blocks.filter(b => b.phase === 'Pending').length;
  const lastRec = stats.lastReconcileTime ? Math.max(0, Math.round((now - Date.parse(stats.lastReconcileTime)) / 1000)) : null;

  return (
    <div className="p1-container">
      <header className="p1-header">
        <div className="p1-title-group">
          <span className="p1-badge">P1</span>
          <div>
            <div className="p1-title"><Boxes size={18} /> Buddy-style dynamic pod IPAM <span className="p1-live"><i /> LIVE</span></div>
            <div className="p1-subtitle">
              Reads the cluster's <code>IPPool</code>, <code>NodeBlock</code>, nodes and pods through the API proxy. Buttons create real pods
              and real lease files; the controller and agents respond.
            </div>
          </div>
        </div>
        <div className="p1-metrics">
          <div className="p1-pill"><span className="label">API</span><span className={`val ${conn === 'ok' ? 'good' : 'warn'}`}>{conn === 'ok' ? 'connected' : conn}</span></div>
          <div className="p1-pill"><span className="label">Agents</span><span className={`val ${agents.total && agents.ready === agents.total ? 'good' : 'warn'}`}>{agents.ready}/{agents.total}</span></div>
          <div className="p1-pill"><span className="label">Pool</span><span className={`val ${stats.phase === 'Ready' ? 'good' : 'warn'}`}>{stats.phase ?? '—'}</span></div>
          <div className="p1-pill"><span className="label">Blocks</span><span className="val">{totalBlocks}{pending ? ` (${pending} pending)` : ''}</span></div>
          <div className="p1-pill" title="The controller updates status only when something changes"><span className="label">Last pool change</span><span className="val">{lastRec === null ? '—' : `${lastRec}s ago`}</span></div>
          <a className="p1-link" href="/project1/sim">Offline simulator</a>
          <a className="p1-link" href="/">Cluster view</a>
          <a className="p1-link" href="/project2">Problem 2</a>
        </div>
      </header>

      {conn === 'error' && (
        <div className="p1-banner">
          Cannot reach the Kubernetes API ({err}). Start the proxy for the cluster the operator manages, for example{' '}
          <code>KUBECONFIG=… kubectl proxy --port 8001</code>, and run the dashboard with the same <code>KUBECONFIG</code> so the buttons act on it.
        </div>
      )}
      {conn === 'ok' && !pool && (
        <div className="p1-banner">No <code>IPPool</code> found. Apply <code>operator/config/samples/ipam_sample.yaml</code> and run <code>go run ./cmd --controllers=ipam</code>.</div>
      )}

      <div className="p1-toolbar">
        <button onClick={refresh}><RefreshCw size={14} />Refresh</button>
        <span className="sep" />
        <FlaskConical size={14} color="#94a3b8" />
        <span className="muted-label">Synthetic usage</span>
        <input type="number" min={1} max={600} value={leases} onChange={e => setLeases(Number(e.target.value))} className="p1-num" aria-label="lease count" />
        <span className="muted-label">leases per click (counted as usage; reclaimed as leaks after ~2 min)</span>
        <span className="sep" />
        <button disabled={busy} onClick={() => act('/api/fault/ipam/clear-load', {})}><Trash2 size={14} />Remove all load pods</button>
        {last && (
          <span className={`p1-last ${last.ok ? 'ok' : 'bad'}`} title={last.output}>
            {last.ok ? '✓' : '✗'} {last.command}{last.output ? ` — ${last.output.slice(0, 80)}` : ''}
          </span>
        )}
      </div>

      <div className="p1-main">
        <section className="p1-panel">
          <div className="p1-panel-title">Address pool <span>read from IPPool/NodeBlock; colour = owning node</span></div>
          {cidrs.map(c => <PoolGrid key={c} cidr={c} blocks={blocks} colorOf={colorOf} hover={hover} setHover={setHover} />)}
          <div className="p1-legend">
            <span><i className="p1-cell free" /> free</span>
            <span><i className="p1-cell ph-Active" style={{ ['--c' as string]: '#94a3b8' }} /> Active</span>
            <span><i className="p1-cell ph-Pending" style={{ ['--c' as string]: '#94a3b8' }} /> Pending</span>
            <span><i className="p1-cell ph-Draining drain" style={{ ['--c' as string]: '#94a3b8' }} /> Draining</span>
            <span><i className="p1-cell ph-Quarantined" style={{ ['--c' as string]: '#94a3b8' }} /> Quarantined</span>
          </div>
          {stats.message && <p className="p1-hint">Controller: {stats.message}</p>}
          <p className="p1-hint">
            Parameters: grow at {pool?.spec?.expandThresholdPercent ?? 80}% / floor {pool?.spec?.triggerFloor ?? 8}, shrink ≤ {pool?.spec?.shrinkThresholdPercent ?? 50}%
            after {pool?.spec?.shrinkCooldownSeconds ?? 300}s, quarantine {pool?.spec?.quarantineSeconds ?? 60}s.
          </p>
        </section>

        <section className="p1-panel p1-nodes">
          <div className="p1-panel-title">Nodes <span>real pods run on the node; the agent counts leases per block</span></div>
          {nodes.map(n => {
            const blks = blocks.filter(b => b.node === n.name).sort((a, b) => a.base - b.base);
            const active = blks.filter(b => b.phase === 'Active' && !b.drain);
            const cap = aggregate(active).reduce((t, r) => t + usable(r.size), 0);
            const used = blks.reduce((t, b) => t + b.used, 0);
            const want = podsWanted[n.name] ?? 20;
            return (
              <div key={n.name} className="p1-node" style={{ ['--c' as string]: colorOf(n.name) }}>
                <div className="p1-node-head">
                  <b>{n.name}</b>
                  <span className="muted">{n.ip} · max-pods {n.maxPods}</span>
                  <span className={`p1-unmet ${n.ready ? '' : 'on'}`}>{n.ready ? `${podsOn(n.name)} pods running` : 'NotReady'}</span>
                </div>
                <div className="p1-slider">
                  <input type="range" min={0} max={150} value={want} onChange={e => setPodsWanted({ ...podsWanted, [n.name]: Number(e.target.value) })} />
                  <span>{want} real pods</span>
                  <button className="p1-act" disabled={busy} onClick={() => act('/api/fault/ipam/load', { nodeName: n.name, pods: want })}><Rocket size={12} />Run</button>
                  <button className="p1-act" disabled={busy} onClick={() => act('/api/fault/ipam/leases', { nodeName: n.name, count: leases })}><FlaskConical size={12} />+{leases}</button>
                </div>
                <div className="p1-bar" title={`${used} leases counted of ${cap} usable`}>
                  <div className="fill" style={{ width: `${cap ? Math.min(100, (100 * used) / cap) : 0}%` }} />
                  <span>{used} used / {cap} usable{aggregate(active).length < active.length ? ' · buddies merged into one range' : ''}</span>
                </div>
                <div className="p1-chips">
                  {blks.map(b => (
                    <span key={b.name} className={`chip ph-${b.phase}${b.drain ? ' drain' : ''}`} onMouseEnter={() => setHover(b.name)} onMouseLeave={() => setHover(null)}
                      title={`routes acked by: ${b.acked.join(', ') || 'nobody yet'}${b.reclaimed ? ` · ${b.reclaimed} leaked leases reclaimed` : ''}`}>
                      {b.cidr} · {b.drain && b.phase === 'Active' ? 'Draining' : b.phase} · {b.used}/{usable(b.size)}
                    </span>
                  ))}
                  {!blks.length && <span className="muted">no block yet — waiting for the controller</span>}
                </div>
              </div>
            );
          })}
        </section>
      </div>

      <section className="p1-bottom">
        <div className="p1-tabs">
          <button className={tab === 'log' ? 'on' : ''} onClick={() => setTab('log')}><ScrollText size={14} />Controller events</button>
          <button className={tab === 'eval' ? 'on' : ''} onClick={() => setTab('eval')}><BarChart3 size={14} />Evaluation</button>
          <button className={tab === 'how' ? 'on' : ''} onClick={() => setTab('how')}><BookOpen size={14} />How it works</button>
        </div>
        {tab === 'log' && <EventView rows={events} />}
        {tab === 'eval' && <EvalView />}
        {tab === 'how' && <HowView />}
      </section>
    </div>
  );
}

function PoolGrid({ cidr, blocks, colorOf, hover, setHover }: {
  cidr: string; blocks: Blk[]; colorOf: (n: string) => string; hover: string | null; setHover: (s: string | null) => void;
}) {
  const { base, size } = parseCidr(cidr);
  const cell = Math.max(16, size / 256);
  const n = size / cell;
  const cells: (Blk | null)[] = new Array(n).fill(null);
  blocks.forEach(b => {
    if (b.base < base || b.base >= base + size) return;
    for (let a = b.base; a < b.base + b.size; a += cell) cells[(a - base) / cell] = cells[(a - base) / cell] ?? b;
    if (b.size < cell) cells[(b.base - base) / cell] = b;
  });
  const free = cells.filter(c => !c).length;
  return (
    <div className="p1-poolgrid">
      <div className="p1-grid-title">{cidr} <span>1 square = {cell} addresses · {(100 * (n - free) / n).toFixed(1)}% allocated</span></div>
      <div className="p1-grid" role="img" aria-label={`Address map of ${cidr}`}>
        {cells.map((b, i) => (
          <div
            key={i}
            className={b ? `p1-cell ph-${b.phase}${b.drain ? ' drain' : ''}${hover === b.name ? ' hl' : ''}` : 'p1-cell free'}
            style={b ? { ['--c' as string]: colorOf(b.node) } : undefined}
            title={b ? `${b.cidr} → ${b.node} (${b.phase}${b.drain ? ', draining' : ''}, ${b.used} used)` : `free: ${numToIp(base + i * cell)}/${32 - Math.log2(cell)}`}
            onMouseEnter={() => b && setHover(b.name)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </div>
    </div>
  );
}

function EventView({ rows }: { rows: EventRow[] }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (ref.current) ref.current.scrollTop = ref.current.scrollHeight; }, [rows.length]);
  return (
    <div className="p1-log" ref={ref}>
      {!rows.length && <div className="line">No IPPool events yet. They appear when the controller allocates, drains or releases a block (Kubernetes keeps events for about an hour).</div>}
      {rows.map((r, i) => (
        <div key={i} className={`line ${r.kind === 'kubelet' ? 'warn' : r.reason === 'BlockAllocated' ? 'grow' : r.reason === 'BlockReleased' || r.reason === 'BlockDraining' ? 'shrink' : ''}`}>
          <span className="t">{new Date(r.time).toLocaleTimeString('en-GB')}</span>
          [{r.kind === 'kubelet' ? 'kubelet' : 'controller'} · {r.reason}] {r.message}
        </div>
      ))}
    </div>
  );
}

function Bars({ title, unit, values, max }: { title: string; unit: string; values: number[][]; max: number }) {
  return (
    <div className="p1-bars">
      <div className="p1-bars-title">{title}</div>
      {scenarios.map((sc, i) => (
        <div key={sc.scenario} className="p1-bar-group">
          <div className="p1-bar-label">{sc.scenario}<span>{sc.note}</span></div>
          {policies.map((p, j) => (
            <div key={p} className="p1-hbar" title={`${p}: ${values[i][j]}${unit}`}>
              <i style={{ width: `${(100 * values[i][j]) / max}%`, background: POLICY_COLORS[j] }} />
              <span>{values[i][j]}{unit}</span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

function EvalView() {
  return (
    <div className="p1-eval">
      <div className="p1-eval-legend">
        {policies.map((p, i) => <span key={p}><i style={{ background: POLICY_COLORS[i] }} />{p}</span>)}
        <em>measured by operator/cmd/ipam-sim, which runs the operator's real Go planner against these baselines (a model of the workload, not a cluster)</em>
      </div>
      <div className="p1-eval-cols">
        <Bars title="Pods that got an address" unit="%" values={scenarios.map(s => s.served)} max={100} />
        <Bars title="Pods per allocated address" unit="%" values={scenarios.map(s => s.util)} max={100} />
        <div className="p1-bars">
          <div className="p1-bars-title">Pods served as the cluster grows (60 pods/node, /16 pool)</div>
          <table className="p1-table">
            <thead><tr><th>nodes</th>{policies.map(p => <th key={p}>{p}</th>)}</tr></thead>
            <tbody>
              {sweep.nodes.map((n, i) => (
                <tr key={n}><td>{n}</td>{policies.map(p => <td key={p} className={sweep.served[p][i] < 100 ? 'bad' : ''}>{sweep.served[p][i]}%</td>)}</tr>
              ))}
            </tbody>
          </table>
          <p className="p1-hint">
            Buddy serves every pod the pool can hold and wastes far less than a fixed /24, but Calico-style blocks pack as well or
            better when load changes (wave 52% vs 41%, burst 38% vs 35%). Buddy's case is allocation in the background, off the
            pod-start path. Shrink is as slow as pod turnover.
          </p>
          <div className="p1-bars-title" style={{ marginTop: 14 }}>Checked by hand on the 3-node minikube cluster</div>
          <ul className="p1-checks">
            {liveChecks.map(c => <li key={c.title}><b>{c.title}.</b> {c.result}</li>)}
          </ul>
        </div>
      </div>
    </div>
  );
}

function HowView() {
  return (
    <div className="p1-how">
      <div>
        <h4>Controller (decides)</h4>
        <ul>
          <li>Pool = buddy tree over the IPPool CIDRs. State lives in <code>NodeBlock</code> objects; a restart rebuilds it.</li>
          <li>New node: first block = 25% of its max pods, rounded to a power of two, placed so its buddy stays free.</li>
          <li>Grow when free &lt; max(8, 20% of capacity), or when the kubelet reports "no IP addresses available". Take the node's own free buddy first.</li>
          <li>Shrink: drain the emptiest block if the rest stays under 50% used; wait until empty; quarantine, then release.</li>
        </ul>
      </div>
      <div>
        <h4>Node agent (makes it true)</h4>
        <ul>
          <li>Installs a route to every other node's blocks and acknowledges them. Order: routes, then CNI config, then Active.</li>
          <li>Writes the <code>ptp</code> + <code>host-local</code> config; buddy blocks become one range.</li>
          <li>Drains blocks, reclaims leaked leases, cleans stale CNI NAT rules, reports usage from lease files.</li>
        </ul>
      </div>
      <div>
        <h4>Things to try on this page</h4>
        <ul>
          <li><b>Run</b> N real pods on a node: they take addresses from that node's block.</li>
          <li><b>+N leases</b>: synthetic usage inside the node's blocks (minikube cannot run hundreds of pods). Watch a block go Pending → Active.</li>
          <li>Remove the load and wait: idle blocks drain, quarantine and are released.</li>
          <li>Synthetic leases are not pods, so the agent removes them as leaks after about 2 minutes.</li>
        </ul>
      </div>
    </div>
  );
}

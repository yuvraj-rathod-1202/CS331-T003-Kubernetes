import { useCallback, useEffect, useRef, useState } from 'react';
import { Boxes, TrendingUp, TrendingDown, Trash2, Eraser, ChevronDown, ChevronUp, CheckCircle2, Circle, Loader2 } from 'lucide-react';
import { aggregate, usable } from '../ipam/engine';
import { policies, scenarios, sweep, liveChecks } from '../ipam/evalData';
import './Project1Demo.css';
import './Project1Live.css';

// Live view of the buddy IPAM operator. The demo is a real workload: a Deployment of real pods is
// scaled out and back in, every pod takes a real address from its node's block, and the controller and
// node agents react. Everything on this page is read from the cluster with kubectl
// (/api/fault/ipam/status); the stages light up when the data shows them, never on a timer.
// The offline simulator is at /project1/sim.

const CONTROLLER_STALE_SECONDS = 75;
const POLL_MS = 1500;
const POLICY_COLORS = ['#2f5d9e', '#b0702d', '#4f8a67', '#8a6aa8'];
const MAX_LOG = 400;

type Scenario = 'cluster' | 'hot';
const SCENARIOS: Record<Scenario, { label: string; button: string; pods: number; text: string }> = {
  cluster: {
    label: 'Fill the IP pool', button: 'Fill the IP pool', pods: 150,
    text: 'A Deployment scales out to 150 real pods across all nodes. Each pod takes an address from its node, the nodes fill up, and the controller has to find them more space.',
  },
  hot: {
    label: 'Crowd one node', button: 'Crowd one node', pods: 70,
    text: 'A Deployment pinned to one node scales out to 70 pods there. Only that node runs out of addresses; the others are left alone.',
  },
};

interface Blk { name: string; node: string; cidr: string; phase: string; drain: boolean; used: number; acked: string[]; base: number; size: number }
interface NodeInfo { name: string; ready: boolean; ip: string; maxPods: number }
interface PodInfo { name: string; ns: string; node: string; ip: string; phase: string; app: string; terminating: boolean }
interface Pool { name: string; spec: any; status: any }
type Source = 'you' | 'kubectl' | 'cluster' | 'controller' | 'agent' | 'kubelet';
interface LogLine { id: string; t: number; src: Source; tone: 'plain' | 'ok' | 'warn' | 'bad' | 'add' | 'remove'; text: string; note?: string }
interface Ghost { cidr: string; node: string; until: number }
interface Phase { t0: number; baseBlocks: string[]; wanted: number; node?: string; reached: Record<number, number>; releasedCount: number; done: boolean }
interface Runs { scenario: Scenario; node?: string; out?: Phase; in?: Phase }

const ipToNum = (ip: string) => ip.split('.').reduce((a, o) => ((a << 8) | Number(o)) >>> 0, 0) >>> 0;
const parseCidr = (c: string) => { const [ip, bits] = c.split('/'); return { base: ipToNum(ip), size: 2 ** (32 - Number(bits)) }; };
const phaseOf = (b: Blk) => (b.drain && b.phase === 'Active' ? 'Draining' : b.phase);
const nodeCap = (blocks: Blk[]) => aggregate(blocks.filter(b => b.phase === 'Active' && !b.drain)).reduce((t, r) => t + usable(r.size), 0);
const nodeUsed = (blocks: Blk[]) => blocks.reduce((t, b) => t + b.used, 0);
const clock = (ms: number) => new Date(ms).toLocaleTimeString('en-GB');
const demoRunning = (pods: PodInfo[]) => pods.filter(p => p.app === 'ipam-demo' && p.phase === 'Running' && !p.terminating).length;

const PHASE_MEANING: Record<string, string> = {
  Pending: 'allocated; waiting until every node has a route to it',
  Active: 'routes ready on all nodes; pods can take addresses from it',
  Draining: 'out of the CNI config; no new pods; waiting for its pods to end',
  Quarantined: 'empty; routes withdrawn; held back before it can be reused',
};

const STAGES_OUT = ['Request accepted', 'Controller adds blocks', 'New blocks Active', 'All pods running'];
const STAGES_IN = ['Request accepted', 'Blocks drained', 'Blocks quarantined', 'Blocks released'];

async function getJson(path: string) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
  return r.json();
}

// Agent log lines look like: 2026-11-04T16:15:36Z \t INFO \t ipam-agent.ipam-agent \t block Active \t {"cidr": "10.244.2.0/24"}
function parseAgentLine(raw: string): { t: number; text: string } | null {
  const parts = raw.split('\t');
  if (parts.length < 4) return null;
  const t = Date.parse(parts[0]);
  if (Number.isNaN(t)) return null;
  let extra = '';
  if (parts[4]) {
    try {
      const o = JSON.parse(parts[4]);
      extra = Object.entries(o).map(([k, v]) => `${k}=${Array.isArray(v) ? (v as unknown[]).join(',') : String(v)}`).join(' ');
    } catch { extra = parts[4]; }
  }
  return { t, text: `${parts[3]}${extra ? ` ${extra}` : ''}` };
}

export function Project1Demo() {
  const [conn, setConn] = useState<'connecting' | 'ok' | 'error'>('connecting');
  const [err, setErr] = useState('');
  const [nodes, setNodes] = useState<NodeInfo[]>([]);
  const [blocks, setBlocks] = useState<Blk[]>([]);
  const [pods, setPods] = useState<PodInfo[]>([]);
  const [pool, setPool] = useState<Pool | null>(null);
  const [agents, setAgents] = useState({ ready: 0, total: 0 });
  const [crdMissing, setCrdMissing] = useState(false);
  const [now, setNow] = useState(Date.now());
  const [log, setLog] = useState<LogLine[]>([]);
  const [ghosts, setGhosts] = useState<Ghost[]>([]);
  const [runs, setRuns] = useState<Runs | null>(null);
  const [autoRelease, setAutoRelease] = useState(true);
  const [hotNode, setHotNode] = useState('');
  const [busy, setBusy] = useState(false);
  const [more, setMore] = useState<'none' | 'eval' | 'how'>('none');
  const [baseline, setBaseline] = useState<{ pods: number; blocks: number; held: number } | null>(null);

  const alive = useRef(true);
  const prev = useRef<{ blocks: Map<string, Blk>; used: Map<string, number>; ranges: Map<string, number>; running: number } | null>(null);
  const firstSeen = useRef<Map<string, number>>(new Map());
  const phaseSince = useRef<Map<string, number>>(new Map());
  const seen = useRef<Set<string>>(new Set());
  const pageLoaded = useRef(Date.now());
  const runsRef = useRef<Runs | null>(null);
  runsRef.current = runs;
  const lastRunningLogged = useRef(-1);

  const addLog = useCallback((items: LogLine[]) => {
    if (!items.length) return;
    setLog(l => [...l, ...items].sort((a, b) => a.t - b.t).slice(-MAX_LOG));
  }, []);

  const refresh = useCallback(async () => {
    try {
      const st = await getJson('/api/fault/ipam/status');
      if (!alive.current) return;
      if (!st.success) throw new Error(st.error ?? 'status failed');
      const t = Date.now();

      const ns: NodeInfo[] = st.nodes.map((n: any) => ({
        name: n.metadata.name,
        ready: n.status.conditions?.find((c: any) => c.type === 'Ready')?.status === 'True',
        ip: n.status.addresses?.find((a: any) => a.type === 'InternalIP')?.address ?? '',
        maxPods: Number(n.status.allocatable?.pods ?? 110),
      })).sort((a: NodeInfo, b: NodeInfo) => a.name.localeCompare(b.name));
      const bs: Blk[] = st.blocks.map((b: any) => {
        const c = parseCidr(b.spec.cidr);
        return {
          name: b.metadata.name, node: b.spec.nodeName, cidr: b.spec.cidr, phase: b.status?.phase || 'Pending',
          drain: !!b.spec.drain, used: b.status?.usedAddresses ?? 0, acked: b.status?.routesAckedBy ?? [], base: c.base, size: c.size,
        } as Blk;
      }).sort((a: Blk, b: Blk) => a.base - b.base);
      const pools = [...st.pools].sort((a: any, b: any) => String(a.metadata.creationTimestamp).localeCompare(String(b.metadata.creationTimestamp)));
      const podRows: PodInfo[] = st.pods;
      const running = demoRunning(podRows);

      // ---- log: what changed since the last poll -------------------------------------------
      const items: LogLine[] = [];
      const p = prev.current;
      const cur = new Map(bs.map(b => [b.name, b]));
      const usedBy = new Map<string, number>();
      const rangesBy = new Map<string, number>();
      for (const n of ns) {
        const mine = bs.filter(b => b.node === n.name);
        usedBy.set(n.name, nodeUsed(mine));
        rangesBy.set(n.name, aggregate(mine.filter(b => b.phase === 'Active' && !b.drain)).length);
      }
      const newGhosts: Ghost[] = [];
      if (p) {
        for (const b of bs) {
          const before = p.blocks.get(b.name);
          if (!before) { firstSeen.current.set(b.name, t); continue; }
          if (phaseOf(before) !== phaseOf(b)) {
            const ph = phaseOf(b);
            items.push({
              id: `ph-${b.name}-${ph}-${t}`, t, src: 'cluster',
              tone: ph === 'Active' ? 'ok' : ph === 'Pending' ? 'warn' : 'plain',
              text: `block ${b.cidr} (${b.node}): ${phaseOf(before)} → ${ph}`, note: PHASE_MEANING[ph],
            });
          }
        }
        for (const b of p.blocks.values()) {
          if (!cur.has(b.name)) newGhosts.push({ cidr: b.cidr, node: b.node, until: t + 9000 });
        }
        for (const n of ns) {
          const a = p.used.get(n.name) ?? 0, z = usedBy.get(n.name) ?? 0;
          if (Math.abs(z - a) >= 8) {
            items.push({ id: `use-${n.name}-${t}`, t, src: 'cluster', tone: z > a ? 'warn' : 'plain', text: `${n.name}: ${z} addresses in use (was ${a}), ${nodeCap(bs.filter(b => b.node === n.name))} usable` });
          }
          const ra = p.ranges.get(n.name) ?? 0, rz = rangesBy.get(n.name) ?? 0;
          const nb = bs.filter(b => b.node === n.name && b.phase === 'Active' && !b.drain).length;
          if (rz !== ra && nb > rz) {
            items.push({ id: `mg-${n.name}-${t}`, t, src: 'cluster', tone: 'ok', text: `${n.name}: ${nb} blocks now served as ${rz} range${rz > 1 ? 's' : ''}`, note: 'neighbouring (buddy) blocks merge, so the 3 reserved addresses are paid once' });
          }
        }
        if (running !== p.running) {
          const want = runsRef.current?.out?.wanted ?? 0;
          const step = lastRunningLogged.current;
          if (running === want || running === 0 || Math.abs(running - step) >= 10) {
            lastRunningLogged.current = running;
            items.push({ id: `pods-${t}`, t, src: 'cluster', tone: 'plain', text: `real pods Running: ${running}${want && runsRef.current?.out && !runsRef.current.in ? ` of ${want}` : ''}` });
          }
        }
      } else {
        for (const b of bs) firstSeen.current.set(b.name, 0);
        lastRunningLogged.current = running;
      }
      for (const b of bs) {
        const ph = phaseOf(b);
        if (p && p.blocks.get(b.name) && phaseOf(p.blocks.get(b.name)!) !== ph) phaseSince.current.set(b.name, t);
        else if (!phaseSince.current.has(b.name)) phaseSince.current.set(b.name, 0);
      }
      // controller events (its own words) and kubelet failures that happened after the page opened.
      // An event that repeats is updated in place (its count goes up), so count is part of the key.
      const starvedBy = new Map<string, number>();
      let starvedAt = 0;
      for (const e of st.events as any[]) {
        const id = `${e.metadata?.uid ?? `${e.reason}-${e.message}`}-${e.count ?? 1}`;
        if (seen.current.has(id)) continue;
        seen.current.add(id);
        const et = Date.parse(e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp);
        if (et < pageLoaded.current - 3000) continue;
        if (e.involvedObject?.kind === 'IPPool') {
          const msg = String(e.message).replace(/^\(combined from similar events\): /, '');
          items.push({ id: `ev-${id}`, t: et, src: 'controller', tone: e.reason === 'BlockAllocated' ? 'add' : e.reason === 'BlockReleased' ? 'remove' : 'plain', text: msg, note: e.reason });
        } else {
          const node = e.source?.host ?? 'node';
          starvedBy.set(node, (starvedBy.get(node) ?? 0) + 1);
          starvedAt = Math.max(starvedAt, et);
        }
      }
      // Pods that cannot get an address are reported once per node per poll, not one line each.
      for (const [node, count] of starvedBy) {
        items.push({ id: `kl-${node}-${t}`, t: starvedAt || t, src: 'kubelet', tone: 'bad', text: `${node}: ${count} pod${count === 1 ? '' : 's'} could not start, no IP addresses available`, note: 'the node is full; the controller treats this as an emergency signal and grows the node' });
      }
      // the node agents' real log lines
      for (const a of st.agentLog as { node: string; line: string }[]) {
        const id = `ag-${a.node}-${a.line}`;
        if (seen.current.has(id)) continue;
        seen.current.add(id);
        const parsed = parseAgentLine(a.line);
        if (!parsed || parsed.t < pageLoaded.current - 3000) continue;
        items.push({ id, t: parsed.t, src: 'agent', tone: 'plain', text: `${a.node}: ${parsed.text}` });
      }
      prev.current = { blocks: cur, used: usedBy, ranges: rangesBy, running };

      // ---- stages, read from the same data -------------------------------------------------
      const r = runsRef.current;
      if (r) {
        let next: Runs = r;
        const upd = (which: 'out' | 'in', ph: Phase) => { next = { ...next, [which]: ph }; };
        const mine = r.node ? bs.filter(b => b.node === r.node) : bs;
        if (r.out && !r.out.done) {
          const o = r.out, reached = { ...o.reached };
          const el = Math.round((t - o.t0) / 1000);
          const mark = (i: number) => { if (reached[i] === undefined) reached[i] = el; };
          mark(0);
          const fresh = mine.filter(b => !o.baseBlocks.includes(b.cidr));
          if (fresh.length) mark(1);
          if (fresh.length && !mine.some(b => b.phase === 'Pending')) mark(2);
          const mineRunning = r.node ? podRows.filter(x => x.app === 'ipam-demo' && x.node === r.node && x.phase === 'Running' && !x.terminating).length : running;
          if (o.wanted > 0 && mineRunning >= 0.98 * o.wanted) mark(3);
          const done = reached[3] !== undefined;
          if (JSON.stringify(reached) !== JSON.stringify(o.reached) || done !== o.done) upd('out', { ...o, reached, done });
        }
        if (r.in && !r.in.done) {
          const i = r.in, reached = { ...i.reached };
          const el = Math.round((t - i.t0) / 1000);
          const mark = (k: number) => { if (reached[k] === undefined) reached[k] = el; };
          mark(0);
          if (mine.some(b => b.drain || b.phase === 'Draining' || b.phase === 'Quarantined')) mark(1);
          if (mine.some(b => b.phase === 'Quarantined')) mark(2);
          const released = i.baseBlocks.filter(c => !mine.some(b => b.cidr === c)).length;
          if (released > 0) mark(3);
          const done = reached[3] !== undefined && !mine.some(b => b.drain || b.phase === 'Draining' || b.phase === 'Quarantined');
          if (JSON.stringify(reached) !== JSON.stringify(i.reached) || released !== i.releasedCount || done !== i.done) upd('in', { ...i, reached, releasedCount: released, done });
        }
        if (next !== r) setRuns(next);
      }

      setNodes(ns);
      setBlocks(bs);
      setPods(podRows);
      setPool(pools[0] ? { name: pools[0].metadata.name, spec: pools[0].spec, status: pools[0].status ?? {} } : null);
      setCrdMissing(!!st.crdMissing);
      setAgents(st.agents ? { total: st.agents.desired, ready: st.agents.ready } : { total: 0, ready: 0 });
      if (newGhosts.length) setGhosts(g => [...g.filter(x => x.until > t), ...newGhosts]);
      addLog(items);
      setHotNode(h => h || (ns[1]?.name ?? ns[0]?.name ?? ''));
      setConn('ok');
      setNow(t);
    } catch (e: any) {
      if (!alive.current) return;
      setConn('error');
      setErr(e.message);
    }
  }, [addLog]);

  // A new poll starts only after the last one ended. With overlapping polls a slow status call
  // (hundreds of pods) used up the browser's connections, and a button press waited behind them.
  useEffect(() => {
    alive.current = true;
    let timer: ReturnType<typeof setTimeout>;
    const loop = async () => {
      await refresh();
      if (alive.current) timer = setTimeout(loop, POLL_MS);
    };
    loop();
    return () => { alive.current = false; clearTimeout(timer); };
  }, [refresh]);

  // ---- actions: every request is written to the log with the command that ran and its result ----
  const request = async (what: string, body: { replicas: number; nodeName?: string }) => {
    const t0 = Date.now();
    addLog([{ id: `you-${t0}`, t: t0, src: 'you', tone: 'plain', text: what, note: `POST /api/fault/ipam/scale ${JSON.stringify(body)}` }]);
    try {
      const r = await fetch('/api/fault/ipam/scale', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const j = await r.json();
      const ok = r.ok && j.success !== false;
      addLog([
        { id: `cmd-${t0}`, t: Date.now(), src: 'kubectl', tone: 'plain', text: j.command ?? 'scale' },
        { id: `res-${t0}`, t: Date.now() + 1, src: 'kubectl', tone: ok ? 'ok' : 'bad', text: ok ? String(j.output ?? 'ok') : `failed: ${j.error ?? r.status}` },
      ]);
      return ok;
    } catch (e: any) {
      addLog([{ id: `res-${t0}`, t: Date.now(), src: 'kubectl', tone: 'bad', text: `request failed: ${e.message}` }]);
      return false;
    }
  };

  const scaleOut = async (scenario: Scenario) => {
    if (busy) return;
    const node = scenario === 'hot' ? hotNode : undefined;
    const target = SCENARIOS[scenario].pods;
    setBusy(true);
    const t = Date.now();
    setBaseline({ pods: demoRunning(pods), blocks: blocks.length, held: blocks.reduce((s, b) => s + b.size, 0) });
    const mine = node ? blocks.filter(b => b.node === node) : blocks;
    const ok = await request(`${SCENARIOS[scenario].button}: scale out to ${target} pods ${node ? `on ${node}` : 'across all nodes'}`, { replicas: target, nodeName: node });
    if (ok) setRuns({ scenario, node, out: { t0: t, baseBlocks: mine.map(b => b.cidr), wanted: target, node, reached: {}, releasedCount: 0, done: false } });
    setBusy(false);
    refresh();
  };
  const scaleIn = async () => {
    if (busy) return;
    setBusy(true);
    const t = Date.now();
    const sc = runs?.scenario ?? 'cluster', nd = runs?.node;
    const mine = nd ? blocks.filter(b => b.node === nd) : blocks;
    const ok = await request(`Free the pool: scale in to 0 pods ${nd ? `on ${nd}` : 'across all nodes'}`, { replicas: 0, nodeName: nd });
    if (ok) setRuns(r => ({ scenario: sc, node: nd, out: r?.out, in: { t0: t, baseBlocks: mine.map(b => b.cidr), wanted: 0, node: nd, reached: {}, releasedCount: 0, done: false } }));
    setBusy(false);
    refresh();
  };
  const scaleInRef = useRef<(() => void) | null>(null);
  scaleInRef.current = scaleIn;
  const removeWorkload = async () => {
    setBusy(true);
    const t = Date.now();
    addLog([{ id: `you-${t}`, t, src: 'you', tone: 'plain', text: 'Remove the demo workload', note: 'POST /api/fault/ipam/clear-load' }]);
    const r = await fetch('/api/fault/ipam/clear-load', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }).then(x => x.json()).catch(() => ({ success: false, error: 'request failed' }));
    addLog([{ id: `cmd-${t}`, t: Date.now(), src: 'kubectl', tone: r.success ? 'ok' : 'bad', text: `${r.command ?? 'delete'}: ${r.output ?? r.error ?? ''}` }]);
    setRuns(null);
    setBaseline(null);
    setBusy(false);
    refresh();
  };
  const clearLog = () => { setLog([]); setGhosts([]); };

  // ---- derived ----------------------------------------------------------------------------
  const lastRec = pool?.status?.lastReconcileTime ? Math.max(0, Math.round((now - Date.parse(pool.status.lastReconcileTime)) / 1000)) : null;
  const controllerRunning = lastRec !== null && lastRec <= CONTROLLER_STALE_SECONDS;
  const notices: string[] = [];
  if (conn === 'ok') {
    if (crdMissing) notices.push('The IPAM CRDs are not installed. Run: kubectl apply -f operator/config/crd/bases/');
    else if (!pool) notices.push('No IPPool found. Apply operator/config/samples/ipam_sample.yaml (set cidrs and reservedCIDRs for this cluster).');
    else if (!controllerRunning) notices.push('The IPAM controller is not running (no status update for over a minute). Start it: cd operator && go run ./cmd --controllers=ipam');
    if (agents.total === 0) notices.push('The ipam-agent DaemonSet is not deployed: kubectl apply -f operator/deploy/ipam-agent.yaml');
    else if (agents.ready < agents.total) notices.push(`Only ${agents.ready} of ${agents.total} IPAM agents are ready; blocks stay Pending until every node has its routes.`);
  }
  const ready = conn === 'ok' && controllerRunning && agents.total > 0 && agents.ready === agents.total;
  const outDone = !!runs?.out?.done && !runs.in;
  const releaseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    if (outDone && autoRelease && !busy && !releaseTimer.current) {
      releaseTimer.current = setTimeout(() => { releaseTimer.current = null; scaleInRef.current?.(); }, 7000);
    }
    if (!outDone && releaseTimer.current) { clearTimeout(releaseTimer.current); releaseTimer.current = null; }
  }, [outDone, autoRelease, busy]);
  const outRunning = !!runs?.out && !runs.out.done;
  const inRunning = !!runs?.in && !runs.in.done;
  const podsRunning = demoRunning(pods);
  const held = blocks.reduce((s, b) => s + b.size, 0);
  const activeGhosts = ghosts.filter(g => g.until > now);
  const stageState = (grp: Phase | undefined, i: number, runningNow: boolean) => {
    if (!grp) return 'todo';
    if (grp.reached[i] !== undefined) return 'done';
    const firstOpen = [0, 1, 2, 3].find(k => grp.reached[k] === undefined);
    return runningNow && firstOpen === i ? 'now' : 'todo';
  };
  const stageNote = (which: 'out' | 'in', i: number) => {
    const g = runs?.[which];
    if (!g) return '';
    if (g.reached[i] !== undefined) return `after ${g.reached[i]}s`;
    if (which === 'out' && i === 3) return `${podsRunning} of ${g.wanted} running`;
    if (which === 'in' && i === 3) return g.releasedCount ? `${g.releasedCount} released` : '';
    return '';
  };

  return (
    <div className="pl-page">
      <header className="pl-header">
        <div className="pl-title"><Boxes size={20} /> Dynamic pod IP allocation <span className="pl-live">Live cluster</span></div>
        <div className="pl-status">
          <Pill ok={conn === 'ok'} label="Cluster" value={conn === 'ok' ? 'connected' : conn} />
          <Pill ok={controllerRunning} label="Controller" value={controllerRunning ? 'running' : lastRec === null ? 'no status' : 'stopped'} />
          <Pill ok={agents.total > 0 && agents.ready === agents.total} label="Node agents" value={`${agents.ready}/${agents.total}`} />
          <Pill ok={pool?.status?.phase === 'Ready'} label="Pool" value={pool?.status?.phase ?? '—'} />
          <a href="/project2">Problem 2</a>
          <a href="/">Cluster view</a>
        </div>
      </header>

      {conn === 'error' && <div className="pl-notice">Cannot read the cluster ({err}). Start the dashboard with the KUBECONFIG of the cluster the operator manages: <code>KUBECONFIG=~/buddy.kubeconfig npm run dev</code></div>}
      {notices.map(n => <div key={n} className="pl-notice">{n}</div>)}

      <main className="pl-main">
        <section className="pl-card pl-scenarios">
          <div className="pl-card-head">
            <div>
              <h2>Real-life scenario: the IP pool fills up</h2>
              <p>Pods are real. Each one takes a real address from its node's block. Press a scenario and follow what the operator does in the log below.</p>
            </div>
            <label className="pl-check"><input type="checkbox" checked={autoRelease} onChange={e => setAutoRelease(e.target.checked)} /> Release automatically afterwards</label>
          </div>
          <div className="pl-buttons">
            <div className="pl-scenario">
              <button className="pl-btn primary" onClick={() => scaleOut('cluster')} disabled={!ready || busy || outRunning || inRunning}>
                {outRunning && runs?.scenario === 'cluster' ? <><Loader2 size={17} className="pl-spin" /> Filling…</> : <><TrendingUp size={17} /> {SCENARIOS.cluster.button}</>}
              </button>
              <p>{SCENARIOS.cluster.text}</p>
            </div>
            <div className="pl-scenario">
              <div className="pl-inline">
                <button className="pl-btn" onClick={() => scaleOut('hot')} disabled={!ready || busy || outRunning || inRunning || !hotNode}>
                  {outRunning && runs?.scenario === 'hot' ? <><Loader2 size={17} className="pl-spin" /> Filling…</> : <><TrendingUp size={17} /> {SCENARIOS.hot.button}</>}
                </button>
                <select value={hotNode} onChange={e => setHotNode(e.target.value)} disabled={outRunning || inRunning} aria-label="node">
                  {nodes.map(n => <option key={n.name} value={n.name}>{n.name}</option>)}
                </select>
              </div>
              <p>{SCENARIOS.hot.text}</p>
            </div>
            <div className="pl-scenario pl-after">
              <button className="pl-btn" onClick={scaleIn} disabled={!ready || busy || inRunning || !runs?.out}>
                {inRunning ? <><Loader2 size={17} className="pl-spin" /> Freeing…</> : <><TrendingDown size={17} /> Free the pool now</>}
              </button>
              <button className="pl-btn quiet" onClick={removeWorkload} disabled={busy}><Trash2 size={15} /> Remove workload</button>
              <p>Scales the workload back to 0 so the controller can take idle blocks back.</p>
            </div>
          </div>
          <div className="pl-steps">
            <StageGroup title="Fill" labels={STAGES_OUT} state={(i) => stageState(runs?.out, i, outRunning)} note={(i) => stageNote('out', i)} />
            <StageGroup title="Free" labels={STAGES_IN} state={(i) => stageState(runs?.in, i, inRunning)} note={(i) => stageNote('in', i)} />
          </div>
        </section>

        <section className="pl-kpis">
          <Kpi label="Real pods running" value={podsRunning} base={baseline?.pods} />
          <Kpi label="Blocks in the pool" value={blocks.length} base={baseline?.blocks} />
          <Kpi label="Addresses given to nodes" value={held} base={baseline?.held} />
          <Kpi label="Pool used" value={`${(pool?.status?.totalAddresses ? (100 * (pool.status.allocatedAddresses ?? 0)) / pool.status.totalAddresses : 0).toFixed(2)}%`} />
        </section>

        <section className="pl-card">
          <div className="pl-card-head slim">
            <h2>Cluster</h2>
            <span className="pl-muted">One square = one address. Filled = in use by a pod. Each outlined group is a block the controller gave the node.</span>
          </div>
          <div className="pl-nodes">
            {nodes.map(n => {
              const mine = blocks.filter(b => b.node === n.name);
              const cap = nodeCap(mine), used = nodeUsed(mine);
              const myGhosts = activeGhosts.filter(g => g.node === n.name);
              const act = mine.filter(b => b.phase === 'Active' && !b.drain);
              const rs = aggregate(act);
              const mineDemo = pods.filter(p => p.node === n.name && p.app === 'ipam-demo' && p.phase === 'Running' && !p.terminating).length;
              return (
                <div key={n.name} className="pl-node">
                  <div className="pl-node-head">
                    <h3>{n.name}</h3>
                    <span className="pl-muted">{n.ip}</span>
                  </div>
                  <div className="pl-node-stats">
                    <span><b>{used}</b> addresses in use</span>
                    <span>{cap} available</span>
                    <span>{mineDemo} demo pods</span>
                  </div>
                  <div className="pl-bar"><div style={{ width: `${cap ? Math.min(100, (100 * used) / cap) : 0}%` }} className={cap && used / cap > 0.8 ? 'high' : ''} /></div>
                  <div className="pl-blocks">
                    {mine.map(b => <BlockTile key={b.name} b={b} isNew={(firstSeen.current.get(b.name) ?? 0) > now - 8000} changed={(phaseSince.current.get(b.name) ?? 0) > now - 3000} />)}
                    {myGhosts.map(g => (
                      <div key={g.cidr} className="pl-block released">
                        <div className="pl-block-top"><span className="pl-block-cidr">{g.cidr}</span><span className="pl-tag released">Released</span></div>
                        <div className="pl-block-note">returned to the pool</div>
                      </div>
                    ))}
                    {!mine.length && !myGhosts.length && <div className="pl-muted">No block yet. Waiting for the controller.</div>}
                  </div>
                  {act.length > 1 && rs.length < act.length && <p className="pl-merge">{act.length} blocks are served as {rs.length} range ({rs.map(r => `/${32 - Math.log2(r.size)}`).join(', ')})</p>}
                </div>
              );
            })}
          </div>
        </section>

        <section className="pl-card">
          <div className="pl-card-head slim">
            <h2>What the operator is doing</h2>
            <div className="pl-log-tools">
              <span className="pl-muted">requests, commands, controller events and node agent logs, from the live cluster</span>
              <button className="pl-btn quiet small" onClick={clearLog}><Eraser size={13} /> Clear</button>
            </div>
          </div>
          <LogView lines={log} />
        </section>

        <section className="pl-more">
          <div className="pl-more-bar">
            <button className={more === 'eval' ? 'on' : ''} onClick={() => setMore(more === 'eval' ? 'none' : 'eval')}>{more === 'eval' ? <ChevronUp size={14} /> : <ChevronDown size={14} />} How it compares (simulator)</button>
            <button className={more === 'how' ? 'on' : ''} onClick={() => setMore(more === 'how' ? 'none' : 'how')}>{more === 'how' ? <ChevronUp size={14} /> : <ChevronDown size={14} />} How it works</button>
          </div>
          {more === 'eval' && <EvalView />}
          {more === 'how' && <HowView />}
        </section>
      </main>
    </div>
  );
}

function Pill({ ok, label, value }: { ok: boolean; label: string; value: string }) {
  return <span className={`pl-pill ${ok ? 'ok' : 'bad'}`}><i />{label}: <b>{value}</b></span>;
}

function Kpi({ label, value, base }: { label: string; value: number | string; base?: number }) {
  const delta = typeof value === 'number' && base !== undefined ? value - base : null;
  return (
    <div className="pl-kpi">
      <div className="pl-kpi-label">{label}</div>
      <div className="pl-kpi-value">{value}{delta !== null && delta !== 0 && <span className={`pl-delta ${delta > 0 ? 'up' : 'down'}`}>{delta > 0 ? '+' : ''}{delta}</span>}</div>
    </div>
  );
}

function StageGroup({ title, labels, state, note }: { title: string; labels: string[]; state: (i: number) => string; note: (i: number) => string }) {
  return (
    <div className="pl-group">
      <div className="pl-group-title">{title}</div>
      <ol className="pl-stages">
        {labels.map((l, i) => {
          const s = state(i);
          return (
            <li key={l} className={`pl-stage ${s}`}>
              <span className="pl-stage-icon">{s === 'done' ? <CheckCircle2 size={20} /> : s === 'now' ? <Loader2 size={20} className="pl-spin" /> : <Circle size={20} />}</span>
              <span className="pl-stage-title">{l}</span>
              <span className="pl-stage-sub">{note(i) || ' '}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function BlockTile({ b, isNew, changed }: { b: Blk; isNew: boolean; changed: boolean }) {
  const ph = phaseOf(b);
  const cap = usable(b.size);
  const filled = Math.min(b.used, cap);
  const note = ph === 'Pending' ? `waiting for routes on the other nodes (${b.acked.length} ready)`
    : ph === 'Draining' && b.used > 0 ? `waiting for ${b.used} pod${b.used === 1 ? '' : 's'} to end`
    : PHASE_MEANING[ph];
  return (
    <div className={`pl-block ph-${ph}${isNew ? ' isnew' : ''}${changed ? ' changed' : ''}`}>
      <div className="pl-block-top">
        <span className="pl-block-cidr">{b.cidr}</span>
        <span className={`pl-tag ph-${ph}`}>{isNew && ph === 'Pending' ? 'New · ' : ''}{ph}</span>
      </div>
      <div className="pl-squares" aria-label={`${filled} of ${cap} addresses in use`}>
        {Array.from({ length: cap }, (_, i) => <i key={i} className={i < filled ? 'on' : ''} />)}
      </div>
      <div className="pl-block-foot"><b>{b.used}</b> / {cap} in use</div>
      <div className="pl-block-note">{note}</div>
    </div>
  );
}

function LogView({ lines }: { lines: LogLine[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useEffect(() => { if (ref.current && stick.current) ref.current.scrollTop = ref.current.scrollHeight; }, [lines.length]);
  return (
    <div className="pl-log" ref={ref} onScroll={e => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40; }}>
      {!lines.length && <div className="pl-empty">Nothing yet.<br />Press <b>Fill the IP pool</b>: the request, the command that runs and everything the cluster does in response appear here.</div>}
      {lines.map(l => (
        <div key={l.id} className={`pl-line ${l.tone}`}>
          <span className="pl-line-time">{clock(l.t)}</span>
          <span className={`pl-who ${l.src}`}>{l.src}</span>
          <span className="pl-line-text">{l.text}{l.note && <em>{l.note}</em>}</span>
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
          <li>New node: first block = a percentage of its max pods, placed so its buddy stays free.</li>
          <li>Grow when free addresses fall below max(8, 20% of capacity), or when the kubelet reports "no IP addresses available". The node's own free buddy is taken first.</li>
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
        <h4>What you are watching</h4>
        <ul>
          <li>The Deployment's pods are real: the scheduler places them, the CNI gives each a real address.</li>
          <li>A block stays Draining while any pod still holds an address in it. It only moves on when the last one ends.</li>
          <li>The log lines tagged <b>agent</b> are the node agents' own logs; <b>controller</b> lines are its Kubernetes events.</li>
        </ul>
      </div>
    </div>
  );
}

import { useCallback, useEffect, useRef, useState } from 'react';
import { TrendingUp, TrendingDown, RotateCcw, Eraser, CheckCircle2, Circle, Loader2 } from 'lucide-react';
import { aggregate, usable } from '../ipam/engine';
import './Project1Live.css';

// Live view of the buddy IPAM operator, on one screen:
//   left   - "Inject a fault": real scenarios (a Deployment of real pods scales out / in)
//   middle - every address the nodes own, which pod holds it, coloured by node
//   right  - what is happening, in plain English
// Everything is read from the cluster with kubectl (/api/fault/ipam/status). Nothing is simulated;
// the steps tick when the cluster data shows them. The offline simulator is at /project1/sim.

const CONTROLLER_STALE_SECONDS = 75;
const POLL_MS = 1500;
const MAX_LOG = 300;
const NODE_COLORS = ['#3b6aa8', '#3f8a5c', '#a86a2b', '#7a5a9e', '#2f7f86'];

type Scenario = 'cluster';
const SCENARIOS: Record<Scenario, { button: string; pods: number; text: string }> = {
  cluster: { button: 'Fill the IP pool', pods: 150, text: '150 real pods across all nodes. Every node runs out of addresses.' },
};

interface Blk {
  name: string; node: string; cidr: string; phase: string; drain: boolean; used: number; acked: string[]; base: number; size: number;
  release: string; initial: boolean; created: number;
  // Set on a merged range: the blocks it is made of (names and CIDRs), lowest first.
  members?: { name: string; cidr: string }[];
}
interface NodeInfo { name: string; ready: boolean; ip: string; maxPods: number }
interface PodInfo { name: string; ns: string; node: string; ip: string; phase: string; app: string; workload: string; terminating: boolean }
interface Pool { name: string; spec: any; status: any }
type Who = 'you' | 'kubernetes' | 'controller' | 'agent' | 'node';
interface LogLine { id: string; t: number; who: Who; tone: 'plain' | 'good' | 'warn' | 'bad'; text: string; detail?: string }
interface Phase { t0: number; baseBlocks: string[]; wanted: number; reached: Record<number, number>; releasedCount: number; done: boolean }
interface Runs { scenario: Scenario; node?: string; out?: Phase; in?: Phase }

const ipToNum = (ip: string) => ip.split('.').reduce((a, o) => ((a << 8) | Number(o)) >>> 0, 0) >>> 0;
const numToIp = (n: number) => [n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join('.');
const parseCidr = (c: string) => { const [ip, bits] = c.split('/'); return { base: ipToNum(ip), size: 2 ** (32 - Number(bits)) }; };
const phaseOf = (b: Blk) => (b.drain && b.phase === 'Active' ? 'Draining' : b.phase);
// The part of a block its node hands out: all of it, or the kept half while the other half is given back.
const served = (b: Blk) => {
  if (!b.release) return { base: b.base, size: b.size };
  const h = parseCidr(b.release);
  return h.base === b.base ? { base: b.base + h.size, size: h.size } : { base: b.base, size: h.size };
};
const nodeCap = (blocks: Blk[]) => aggregate(blocks.filter(b => b.phase === 'Active' && !b.drain).map(served)).reduce((t, r) => t + usable(r.size), 0);
const cidrOfRange = (base: number, size: number) => `${numToIp(base)}/${32 - Math.log2(size)}`;

// Blocks as the node uses them: Active buddy blocks of one node are one host-local range (one route,
// 3 reserved addresses once), so they are shown as one merged block. Blocks that are new, being taken
// back, resting, or giving back a half are not part of that range and stay separate.
function mergedView(blocks: Blk[]): Blk[] {
  const out: Blk[] = [];
  const byNode = new Map<string, Blk[]>();
  for (const b of blocks) {
    if (b.phase === 'Active' && !b.drain && !b.release) byNode.set(b.node, [...(byNode.get(b.node) ?? []), b]);
    else out.push(b);
  }
  for (const [node, act] of byNode) {
    for (const r of aggregate(act)) {
      const members = act.filter(b => b.base >= r.base && b.base + b.size <= r.base + r.size).sort((a, b) => a.base - b.base);
      if (members.length === 1) { out.push(members[0]); continue; }
      out.push({
        name: `merged-${r.base}-${r.size}`, node, cidr: cidrOfRange(r.base, r.size), phase: 'Active', drain: false,
        used: members.reduce((t, b) => t + b.used, 0), acked: [], base: r.base, size: r.size, release: '',
        initial: members.some(b => b.initial), created: Math.min(...members.map(b => b.created)),
        members: members.map(b => ({ name: b.name, cidr: b.cidr })),
      });
    }
  }
  return out.sort((a, b) => a.base - b.base);
}

// The node's starting block, which the controller never takes back: the marked one, or else its oldest.
const startingOf = (blocks: Blk[]) => blocks.find(b => b.initial)
  ?? [...blocks].filter(b => !b.drain && b.phase !== 'Quarantined')
    .sort((a, b) => (Number.isFinite(a.created) ? a.created : Infinity) - (Number.isFinite(b.created) ? b.created : Infinity) || a.name.localeCompare(b.name))[0];
const clock = (ms: number) => new Date(ms).toLocaleTimeString('en-GB');
const demoRunning = (pods: PodInfo[]) => pods.filter(p => p.app === 'ipam-demo' && p.phase === 'Running' && !p.terminating).length;
const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;
const listOf = (xs: string[]) => (xs.length <= 1 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`);

const STAGES_OUT = ['Pods requested', 'Nodes run out, controller adds blocks', 'Routes ready, blocks Active', 'All pods running'];
const STAGES_IN = ['Pods removed', 'Controller takes blocks back', 'Empty blocks set aside', 'Blocks back in the pool'];

async function getJson(path: string) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
  return r.json();
}

// ---- turning cluster facts into sentences ------------------------------------------------------

function controllerSentence(reason: string, message: string, short: (n: string) => string): { text: string; tone: LogLine['tone'] } {
  const msg = message.replace(/^\(combined from similar events\): /, '');
  let m = msg.match(/^(initial|grow-in-place|grow-beside|grow) block (\S+) to node (\S+)/);
  if (m) {
    const [, kind, cidr, node] = m;
    const n = usable(parseCidr(cidr).size);
    if (kind === 'initial') return { tone: 'good', text: `Gave ${short(node)} its first block, ${cidr} (${n} addresses).` };
    if (kind === 'grow-beside') return { tone: 'good', text: `${short(node)} was running out of addresses. The controller gave it ${cidr} (+${n} addresses): just what it needs, taken from the free space right next to its block. The rest of that space stays free for its next grow.` };
    if (kind === 'grow-in-place') return { tone: 'good', text: `${short(node)} was running out of addresses. The controller gave it ${cidr}, the free neighbour of a block it already has, so the two join into one bigger range.` };
    return { tone: 'good', text: `${short(node)} was running out of addresses, so the controller gave it a new block ${cidr} (+${n} addresses).` };
  }
  m = msg.match(/^Releasing half (\S+) of block (\S+) of node (\S+)/);
  if (m) return { tone: 'warn', text: `${short(m[3])} has more room than it needs, and half of its block ${m[2]} is empty. The controller is giving back that half, ${m[1]}: no new pod will get an address there. The pods in the other half are not touched.` };
  m = msg.match(/^Kept half (\S+) of block (\S+) of node (\S+)/);
  if (m) return { tone: 'good', text: `${short(m[3])} needs the space again, so the controller keeps all of ${m[2]}.` };
  m = msg.match(/^Split block (\S+) of node (\S+): kept (\S+), (\S+) goes back/);
  if (m) return { tone: 'good', text: `Block ${m[1]} is split: ${short(m[2])} keeps ${m[3]} with its pods, and ${m[4]} rests briefly, then goes back to the pool.` };
  m = msg.match(/block (\S+) of node (\S+)/);
  if (m) {
    const [, cidr, node] = m;
    if (reason === 'BlockDraining') return { tone: 'warn', text: `${short(node)} has more addresses than it needs. The controller is taking ${cidr} back: no new pod will get an address from it.` };
    if (reason === 'BlockRestored') return { tone: 'good', text: `${short(node)} needs ${cidr} again, so the controller stopped taking it back.` };
    if (reason === 'BlockReleased') return { tone: 'plain', text: `${cidr} is back in the pool. Any node can get it now.` };
  }
  m = msg.match(/block (\S+): node (\S+) is gone/);
  if (m) return { tone: 'warn', text: `${short(m[2])} left the cluster, so its block ${m[1]} was set aside.` };
  return { tone: 'plain', text: msg };
}

// Agent log lines look like: 2026-11-04T16:15:36Z \t INFO \t ipam-agent \t route installed \t {"dst": "...", "via": "..."}
function parseAgentLine(raw: string): { t: number; msg: string; f: Record<string, any> } | null {
  const parts = raw.split('\t');
  if (parts.length < 4) return null;
  const t = Date.parse(parts[0]);
  if (Number.isNaN(t)) return null;
  let f: Record<string, any> = {};
  try { if (parts[4]) f = JSON.parse(parts[4]); } catch { /* keep empty */ }
  return { t, msg: parts[3], f };
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
  const [runs, setRuns] = useState<Runs | null>(null);
  const [busy, setBusy] = useState(false);
  // Squares clicked to stop a pod, shown as "stopping" until the pod is gone (or 20 s pass).
  const [stopping, setStopping] = useState<Map<string, number>>(new Map());

  const alive = useRef(true);
  const prev = useRef<{ blocks: Map<string, Blk>; ratio: Map<string, number>; ranges: Map<string, number>; running: number; poolPhase: string } | null>(null);
  const starvedLogged = useRef<Map<string, number>>(new Map());
  const firstSeen = useRef<Map<string, number>>(new Map());
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
          release: b.spec.release ?? '', initial: !!b.spec.initial, created: Date.parse(b.metadata.creationTimestamp),
        } as Blk;
      }).sort((a: Blk, b: Blk) => a.base - b.base);
      const pools = [...st.pools].sort((a: any, b: any) => String(a.metadata.creationTimestamp).localeCompare(String(b.metadata.creationTimestamp)));
      const podRows: PodInfo[] = st.pods;
      const running = demoRunning(podRows);
      const poolPhase = String(pools[0]?.status?.phase ?? '');
      const ipOwner = new Map(ns.map(n => [n.ip, n.name]));
      const short = (n: string) => n;
      const quarantine = Number(pools[0]?.spec?.quarantineSeconds ?? 0);
      // The pool's own grow threshold (expandThresholdPercent), not an assumed 80%.
      const growAt = Number(pools[0]?.spec?.expandThresholdPercent ?? 80) / 100;

      // ---- what changed since the last poll, as sentences ------------------------------------
      const items: LogLine[] = [];
      const p = prev.current;
      const cur = new Map(bs.map(b => [b.name, b]));
      const ratio = new Map<string, number>();
      const ranges = new Map<string, number>();
      for (const n of ns) {
        const mine = bs.filter(b => b.node === n.name);
        const cap = nodeCap(mine);
        ratio.set(n.name, cap ? mine.reduce((s, b) => s + b.used, 0) / cap : 0);
        ranges.set(n.name, aggregate(mine.filter(b => b.phase === 'Active' && !b.drain)).length);
      }
      if (p) {
        // Blocks that change together in one poll are one line, not one line each.
        const ready: Blk[] = [], emptied: Blk[] = [];
        for (const b of bs) {
          const before = p.blocks.get(b.name);
          if (!before) { firstSeen.current.set(b.name, t); continue; }
          const a = phaseOf(before), z = phaseOf(b);
          if (a === z) continue;
          if (z === 'Active' && a === 'Pending') ready.push(b);
          else if (z === 'Quarantined') emptied.push(b);
        }
        for (const n of ns) {
          const g = ready.filter(b => b.node === n.name);
          if (g.length) items.push({ id: `ready-${n.name}-${t}`, t, who: 'agent', tone: 'good', text: `${listOf(g.map(b => b.cidr))} on ${short(n.name)} ${g.length === 1 ? 'is' : 'are'} ready. Every node has a route to ${g.length === 1 ? 'it' : 'them'}, so new pods on ${short(n.name)} can use ${g.length === 1 ? 'its' : 'their'} addresses.` });
        }
        if (emptied.length) items.push({ id: `empty-${t}`, t, who: 'agent', tone: 'plain', text: `The last pod in ${listOf(emptied.map(b => b.cidr))} is gone. ${emptied.length === 1 ? 'Its routes were' : 'Their routes were'} removed and ${emptied.length === 1 ? 'it rests' : 'they rest'}${quarantine ? ` for ${quarantine}s` : ''} before going back to the pool.` });
        for (const n of ns) {
          const a = p.ratio.get(n.name) ?? 0, z = ratio.get(n.name) ?? 0;
          if (a < growAt && z >= growAt) items.push({ id: `full-${n.name}-${t}`, t, who: 'node', tone: 'warn', text: `${short(n.name)} is ${Math.round(z * 100)}% full. Above ${Math.round(growAt * 100)}% the controller gives a node more addresses.` });
          const ra = p.ranges.get(n.name) ?? 0, rz = ranges.get(n.name) ?? 0;
          const nb = bs.filter(b => b.node === n.name && b.phase === 'Active' && !b.drain).length;
          if (rz !== ra && nb > rz) items.push({ id: `mg-${n.name}-${t}`, t, who: 'agent', tone: 'good', text: `On ${short(n.name)}, ${nb} neighbouring blocks now work as ${rz === 1 ? 'one range' : `${rz} ranges`}, so 3 addresses are reserved once instead of ${nb} times.` });
        }
        if (running !== p.running) {
          const r = runsRef.current;
          const want = r?.out && !r.in ? r.out.wanted : 0;
          const step = lastRunningLogged.current;
          if (running === want || running === 0 || Math.abs(running - step) >= 15) {
            lastRunningLogged.current = running;
            items.push({
              id: `pods-${t}`, t, who: 'kubernetes', tone: running === want && want ? 'good' : 'plain',
              text: running === want && want ? `All ${want} pods are running. Every one of them got an IP address.`
                : running === 0 ? 'No demo pods are running any more. Their addresses are free.'
                : want ? `${running} of ${want} pods are running.` : `${running} demo pods still running.`,
            });
          }
        }
        if (poolPhase !== p.poolPhase && poolPhase) {
          const msg = String(pools[0]?.status?.message ?? '');
          if (poolPhase === 'Exhausted') items.push({ id: `pool-${t}`, t, who: 'controller', tone: 'bad', text: `The pool is full. ${(msg.match(/cannot grow node (\S+?):/) ?? [])[1] ?? 'A node'} needs more addresses but there is no block left to give. Its new pods wait until another node gives a block back. Pods that already run keep their IPs.` });
          else if (poolPhase === 'Degraded' && /low on free/.test(msg)) items.push({ id: `pool-${t}`, t, who: 'controller', tone: 'warn', text: `The pool is almost empty: less than ${pools[0]?.spec?.lowWaterPercent ?? 10}% of its addresses are left.` });
          else if (poolPhase === 'Ready' && (p.poolPhase === 'Exhausted' || p.poolPhase === 'Degraded')) items.push({ id: `pool-${t}`, t, who: 'controller', tone: 'good', text: 'The pool has room again. Blocks that nodes gave back can be handed out.' });
        }
      } else {
        for (const b of bs) firstSeen.current.set(b.name, 0);
        lastRunningLogged.current = running;
      }

      // Controller decisions (its Kubernetes events) and pods that could not get an address, since the page opened.
      const starved = new Map<string, number>();
      let starvedAt = 0;
      // Draining blocks (per node) and released blocks arrive in bursts: one line per burst.
      const draining = new Map<string, { cidrs: string[]; t: number }>();
      const released: { cidrs: string[]; t: number } = { cidrs: [], t: 0 };
      for (const e of st.events as any[]) {
        const id = `${e.metadata?.uid ?? `${e.reason}-${e.message}`}-${e.count ?? 1}`;
        if (seen.current.has(id)) continue;
        seen.current.add(id);
        const et = Date.parse(e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp);
        if (et < pageLoaded.current - 3000) continue;
        if (e.involvedObject?.kind === 'IPPool') {
          // PoolExhausted repeats on every pass; the pool's phase change says it once.
          if (e.reason === 'PoolReady' || e.reason === 'PoolExhausted' || /is serving/.test(e.message)) continue;
          const bm = String(e.message).match(/block (\S+) of node (\S+)/);
          if (bm && e.reason === 'BlockDraining') {
            const g = draining.get(bm[2]) ?? { cidrs: [], t: 0 };
            g.cidrs.push(bm[1]); g.t = Math.max(g.t, et);
            draining.set(bm[2], g);
          } else if (bm && e.reason === 'BlockReleased') {
            released.cidrs.push(bm[1]); released.t = Math.max(released.t, et);
          } else {
            const s = controllerSentence(e.reason, String(e.message), short);
            items.push({ id: `ev-${id}`, t: et, who: 'controller', tone: s.tone, text: s.text });
          }
        } else {
          const node = e.source?.host ?? 'a node';
          starved.set(node, (starved.get(node) ?? 0) + 1);
          starvedAt = Math.max(starvedAt, et);
        }
      }
      for (const [node, g] of draining) {
        const one = g.cidrs.length === 1;
        items.push({ id: `drain-${node}-${t}`, t: g.t, who: 'controller', tone: 'warn', text: `${short(node)} has more addresses than it needs. The controller is taking ${listOf(g.cidrs)} back: no new pod will get an address from ${one ? 'it' : 'them'}.` });
      }
      if (released.cidrs.length) {
        const one = released.cidrs.length === 1;
        items.push({ id: `released-${t}`, t: released.t, who: 'controller', tone: 'plain', text: `${listOf(released.cidrs)} ${one ? 'is' : 'are'} back in the pool. Any node can get ${one ? 'it' : 'them'} now.` });
      }
      for (const [node, count] of starved) {
        // While the pool is full the kubelet keeps retrying; one line per node every 15 s is enough.
        if (t - (starvedLogged.current.get(node) ?? 0) < 15000) continue;
        starvedLogged.current.set(node, t);
        items.push({ id: `kl-${node}-${t}`, t: starvedAt || t, who: 'node', tone: 'bad', text: `${short(node)} is out of addresses: ${plural(count, 'new pod')} could not get an IP and ${count === 1 ? 'is' : 'are'} waiting. ${poolPhase === 'Exhausted' ? 'The pool is full, so they wait for a block to come back.' : `The controller takes this as an urgent signal to grow ${short(node)}.`}` });
      }

      // The node agents' own log lines, grouped so one change reads as one sentence.
      const routeAdd = new Map<string, { via: string; on: string[]; t: number }>();
      const routeDel = new Map<string, { via: string; on: string[]; t: number }>();
      for (const a of st.agentLog as { node: string; line: string }[]) {
        const id = `ag-${a.node}-${a.line}`;
        if (seen.current.has(id)) continue;
        seen.current.add(id);
        const x = parseAgentLine(a.line);
        if (!x || x.t < pageLoaded.current - 3000) continue;
        if (x.msg === 'route installed' || x.msg === 'route removed') {
          const map = x.msg === 'route installed' ? routeAdd : routeDel;
          const g = map.get(x.f.dst) ?? { via: String(x.f.via), on: [] as string[], t: x.t };
          g.on.push(short(a.node)); g.t = Math.max(g.t, x.t);
          map.set(x.f.dst, g);
        } else if (x.msg === 'CNI config written') {
          const rs: string[] = x.f.ranges ?? x.f.blocks ?? [];
          items.push({ id, t: x.t, who: 'agent', tone: 'plain', text: `${short(a.node)} updated its CNI config. New pods there now get addresses from ${listOf(rs)}.` });
        } else if (x.msg === 'removed stale CNI masquerade rules') {
          items.push({ id, t: x.t, who: 'agent', tone: 'plain', text: `${short(a.node)} cleaned up ${plural(Number(x.f.rules ?? 0), 'old NAT rule')} left behind by deleted pods.` });
        } else if (x.msg === 'route skipped') {
          items.push({ id, t: x.t, who: 'agent', tone: 'warn', text: `${short(a.node)} could not add a route yet: ${x.f.reason ?? ''}` });
        }
        // "block Active/Draining/Quarantined" lines are covered by the block's phase change above.
      }
      for (const [dst, g] of routeAdd) {
        items.push({ id: `ra-${dst}-${g.t}`, t: g.t, who: 'agent', tone: 'plain', text: `${listOf(g.on)} now ${g.on.length === 1 ? 'sends' : 'send'} traffic for ${dst} to ${short(ipOwner.get(g.via) ?? g.via)}, the node that owns it.` });
      }
      for (const [dst, g] of routeDel) {
        items.push({ id: `rd-${dst}-${g.t}`, t: g.t, who: 'agent', tone: 'plain', text: `${listOf(g.on)} removed the route for ${dst}.` });
      }
      prev.current = { blocks: cur, ratio, ranges, running, poolPhase };

      // ---- the steps, read from the same data -------------------------------------------------
      const r = runsRef.current;
      if (r) {
        let next: Runs = r;
        const mine = r.node ? bs.filter(b => b.node === r.node) : bs;
        if (r.out && !r.out.done) {
          const o = r.out, reached = { ...o.reached };
          const el = Math.round((t - o.t0) / 1000);
          const mark = (i: number) => { if (reached[i] === undefined) reached[i] = el; };
          mark(0);
          const fresh = mine.filter(b => !o.baseBlocks.includes(b.cidr));
          if (fresh.length) mark(1);
          if (fresh.length && !mine.some(b => b.phase === 'Pending')) mark(2);
          if (o.wanted > 0 && running >= 0.98 * o.wanted) mark(3);
          const done = reached[3] !== undefined;
          if (JSON.stringify(reached) !== JSON.stringify(o.reached) || done !== o.done) next = { ...next, out: { ...o, reached, done } };
        }
        if (r.in && !r.in.done) {
          const i = r.in, reached = { ...i.reached };
          const el = Math.round((t - i.t0) / 1000);
          const mark = (k: number) => { if (reached[k] === undefined) reached[k] = el; };
          mark(0);
          if (mine.some(b => b.drain || b.release || b.phase === 'Draining' || b.phase === 'Quarantined')) mark(1);
          if (mine.some(b => b.phase === 'Quarantined')) mark(2);
          const released = i.baseBlocks.filter(c => !mine.some(b => b.cidr === c)).length;
          // Done only when nothing is on its way back and no node still holds an empty block besides its
          // starting one (the controller gives those back over a few passes). A node whose rules keep
          // a block (pods still on it) does not hold the steps open for more than 3 minutes.
          const inProgress = mine.some(b => b.drain || b.release || b.phase === 'Draining' || b.phase === 'Quarantined' || b.phase === 'Pending');
          const leftover = ns.some(n => {
            const nb = mine.filter(b => b.node === n.name);
            const first = startingOf(nb)?.name;
            return nb.some(b => b.name !== first && b.phase === 'Active' && !b.drain && b.used === 0);
          });
          const done = released > 0 && !inProgress && (!leftover || el > 180);
          if (done) mark(3);
          if (JSON.stringify(reached) !== JSON.stringify(i.reached) || released !== i.releasedCount || done !== i.done) next = { ...next, in: { ...i, reached, releasedCount: released, done } };
        }
        if (next !== r) setRuns(next);
      }

      setNodes(ns);
      setBlocks(bs);
      setPods(podRows);
      setPool(pools[0] ? { name: pools[0].metadata.name, spec: pools[0].spec, status: pools[0].status ?? {} } : null);
      setCrdMissing(!!st.crdMissing);
      setAgents(st.agents ? { total: st.agents.desired, ready: st.agents.ready } : { total: 0, ready: 0 });
      addLog(items);
      setConn('ok');
      setNow(t);
    } catch (e: any) {
      if (!alive.current) return;
      setConn('error');
      setErr(e.message);
    }
  }, [addLog]);

  // A new poll starts only after the last one ended, so a button press never waits behind slow polls.
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

  // ---- actions --------------------------------------------------------------------------------
  const request = async (text: string, body: { replicas: number; nodeName?: string }) => {
    const t0 = Date.now();
    addLog([{ id: `you-${t0}`, t: t0, who: 'you', tone: 'plain', text }]);
    try {
      const r = await fetch('/api/fault/ipam/scale', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      const j = await r.json();
      const ok = r.ok && j.success !== false;
      addLog([{
        id: `res-${t0}`, t: Date.now(), who: 'kubernetes', tone: ok ? 'plain' : 'bad',
        text: ok ? (body.replicas > 0 ? 'Kubernetes accepted the request. The scheduler is placing the pods, and each one asks its node for an IP address.' : 'Kubernetes accepted the request and is stopping the pods.') : `The request failed: ${j.error ?? r.status}`,
        detail: j.command,
      }]);
      return ok;
    } catch (e: any) {
      addLog([{ id: `res-${t0}`, t: Date.now(), who: 'kubernetes', tone: 'bad', text: `The request failed: ${e.message}` }]);
      return false;
    }
  };

  // Set the number of demo pods pinned to one node (its own Deployment, web-<node>).
  const setNodePods = async (node: string, n: number) => {
    if (busy) return;
    setBusy(true);
    await request(`You set ${node} to ${n} demo pod${n === 1 ? '' : 's'}.`, { replicas: n, nodeName: node });
    setBusy(false);
    refresh();
  };

  const scaleOut = async (scenario: Scenario) => {
    if (busy) return;
    const target = SCENARIOS[scenario].pods;
    setBusy(true);
    const t = Date.now();
    const ok = await request(`You pressed “${SCENARIOS[scenario].button}”: asking for ${target} pods spread over all nodes.`, { replicas: target });
    if (ok) {
      lastRunningLogged.current = demoRunning(pods);
      setRuns({ scenario, out: { t0: t, baseBlocks: blocks.map(b => b.cidr), wanted: target, reached: {}, releasedCount: 0, done: false } });
    }
    setBusy(false);
    refresh();
  };
  const scaleIn = async () => {
    if (busy) return;
    setBusy(true);
    const t = Date.now();
    const sc = runs?.scenario ?? 'cluster', nd = runs?.node;
    const mine = nd ? blocks.filter(b => b.node === nd) : blocks;
    const ok = await request(`You pressed “Free the pool”: removing all demo pods${nd ? ` on ${nd}` : ''}.`, { replicas: 0, nodeName: nd });
    if (ok) setRuns(r => ({ scenario: sc, node: nd, out: r?.out, in: { t0: t, baseBlocks: mine.map(b => b.cidr), wanted: 0, reached: {}, releasedCount: 0, done: false } }));
    setBusy(false);
    refresh();
  };
  const reset = async () => {
    setBusy(true);
    const t = Date.now();
    addLog([{ id: `you-${t}`, t, who: 'you', tone: 'plain', text: 'You pressed “Reset”: deleting the demo workload.' }]);
    const r = await fetch('/api/fault/ipam/clear-load', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }).then(x => x.json()).catch(() => ({ success: false, error: 'request failed' }));
    addLog([{ id: `res-${t}`, t: Date.now(), who: 'kubernetes', tone: r.success ? 'plain' : 'bad', text: r.success ? 'The demo workload is deleted. The controller takes back the extra blocks on its own.' : `Reset failed: ${r.error ?? ''}`, detail: r.command }]);
    setRuns(null);
    setBusy(false);
    refresh();
  };

  // One click on a pod square stops that pod; one click on a free square starts a pod on that node.
  const stopPod = async (pod: PodInfo) => {
    const t0 = Date.now();
    setStopping(m => new Map(m).set(pod.ip, t0));
    addLog([{ id: `you-stop-${t0}`, t: t0, who: 'you', tone: 'plain', text: `You clicked ${pod.ip}: stopping pod ${pod.name} on ${pod.node}.` }]);
    const j = await fetch('/api/fault/ipam/pod/stop', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: pod.name }) })
      .then(r => r.json()).catch(e => ({ success: false, error: e.message }));
    addLog([{
      id: `res-stop-${t0}`, t: Date.now(), who: 'kubernetes', tone: j.success ? 'plain' : 'bad', detail: j.command,
      text: j.success ? `Kubernetes removes exactly that pod and lowers ${j.workload} to ${j.replicas}, so it is not replaced. ${pod.ip} becomes free.` : `Could not stop the pod: ${j.error ?? 'request failed'}`,
    }]);
    if (!j.success) setStopping(m => { const n = new Map(m); n.delete(pod.ip); return n; });
    refresh();
  };
  const startPod = async (node: string) => {
    const t0 = Date.now();
    addLog([{ id: `you-start-${t0}`, t: t0, who: 'you', tone: 'plain', text: `You clicked a free address on ${node}: starting one more pod there.` }]);
    const j = await fetch('/api/fault/ipam/pod/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ nodeName: node }) })
      .then(r => r.json()).catch(e => ({ success: false, error: e.message }));
    addLog([{
      id: `res-start-${t0}`, t: Date.now(), who: 'kubernetes', tone: j.success ? 'plain' : 'bad', detail: j.command,
      text: j.success ? `Kubernetes accepted it: ${j.workload} now has ${j.replicas} pod${j.replicas === 1 ? '' : 's'}. ${node} gives the new pod its next free address.` : `Could not start a pod: ${j.error ?? 'request failed'}`,
    }]);
    refresh();
  };
  useEffect(() => {
    // A stopped pod's square leaves the "stopping" state once the pod is gone.
    setStopping(m => {
      if (!m.size) return m;
      const n = new Map([...m].filter(([ip, t]) => now - t < 20000 && pods.some(p => p.ip === ip && p.app === 'ipam-demo')));
      return n.size === m.size ? m : n;
    });
  }, [pods, now]);

  // ---- derived ----------------------------------------------------------------------------------
  const lastRec = pool?.status?.lastReconcileTime ? Math.max(0, Math.round((now - Date.parse(pool.status.lastReconcileTime)) / 1000)) : null;
  const controllerRunning = lastRec !== null && lastRec <= CONTROLLER_STALE_SECONDS;
  const notices: string[] = [];
  if (conn === 'error') notices.push(`Cannot read the cluster (${err}). Start the website with KUBECONFIG=~/buddy.kubeconfig npm run dev`);
  if (conn === 'ok') {
    if (crdMissing) notices.push('The IPAM CRDs are not installed: kubectl apply -f operator/config/crd/bases/');
    else if (!pool) notices.push('No IPPool found: apply operator/config/samples/ipam_sample.yaml');
    else if (!controllerRunning) notices.push('The IPAM controller is not running. Start it: cd operator && go run ./cmd --controllers=ipam --health-probe-bind-address=:18081 --metrics-bind-address=0');
    if (agents.total === 0) notices.push('The ipam-agent DaemonSet is not deployed: kubectl apply -f operator/deploy/ipam-agent.yaml');
    else if (agents.ready < agents.total) notices.push(`Only ${agents.ready} of ${agents.total} node agents are ready yet.`);
  }
  const ready = conn === 'ok' && controllerRunning && agents.total > 0 && agents.ready === agents.total;
  const outRunning = !!runs?.out && !runs.out.done;
  const inRunning = !!runs?.in && !runs.in.done;
  const podsRunning = demoRunning(pods);
  const podByIp = new Map(pods.filter(p => !p.terminating || p.phase === 'Running').map(p => [p.ip, p]));
  const inUse = blocks.reduce((s, b) => s + b.used, 0);
  const given = nodes.reduce((s, n) => s + nodeCap(blocks.filter(b => b.node === n.name)), 0);
  const colorOf = (node: string) => NODE_COLORS[Math.max(0, nodes.findIndex(n => n.name === node)) % NODE_COLORS.length];
  const stageState = (grp: Phase | undefined, i: number, runningNow: boolean) => {
    if (!grp) return 'todo';
    if (grp.reached[i] !== undefined) return 'done';
    const firstOpen = [0, 1, 2, 3].find(k => grp.reached[k] === undefined);
    return runningNow && firstOpen === i ? 'now' : 'todo';
  };
  const stageNote = (which: 'out' | 'in', i: number) => {
    const g = runs?.[which];
    if (!g) return '';
    if (g.reached[i] !== undefined) return `${g.reached[i]}s`;
    if (which === 'out' && i === 3) return `${podsRunning}/${g.wanted}`;
    if (which === 'in' && i === 3 && g.releasedCount) return `${g.releasedCount} back`;
    return '';
  };

  return (
    <div className="pl-page">
      <header className="pl-header">
        <div className="pl-title">Problem 1 · Dynamic pod IP allocation <span className="pl-live">live cluster</span></div>
        <div className="pl-status">
          <Pill ok={conn === 'ok'} label="Cluster" value={conn === 'ok' ? 'connected' : conn} />
          <Pill ok={controllerRunning} label="Controller" value={controllerRunning ? 'running' : 'stopped'} />
          <Pill ok={agents.total > 0 && agents.ready === agents.total} label="Node agents" value={`${agents.ready}/${agents.total}`} />
          <Pill ok={pool?.status?.phase === 'Ready'} label="Pool" value={pool?.status?.phase ?? '—'} />
          <a href="/project1/sim">Simulator results</a>
          <a href="/project2">Problem 2</a>
        </div>
      </header>
      {notices.length > 0 && <div className="pl-notices">{notices.map(n => <div key={n}>{n}</div>)}</div>}

      <main className="pl-grid">
        {/* ---- top: inject a fault. The steps appear only once a button has been pressed ---- */}
        <section className="pl-panel pl-fault">
          <div className="pl-bar">
            <h2>Inject a fault</h2>
            <button className="pl-action primary" onClick={() => scaleOut('cluster')} disabled={!ready || busy || outRunning || inRunning}>
              {outRunning && runs?.scenario === 'cluster' ? <Loader2 size={16} className="pl-spin" /> : <TrendingUp size={16} />}
              <span><b>{SCENARIOS.cluster.button}</b></span>
            </button>
            <button className="pl-action" onClick={scaleIn} disabled={!ready || busy || inRunning || !pods.some(p => p.workload === 'web' && !p.terminating)}>
              {inRunning ? <Loader2 size={16} className="pl-spin" /> : <TrendingDown size={16} />}
              <span><b>Free the pool</b></span>
            </button>
            <button className="pl-action quiet" onClick={reset} disabled={busy}>
              <RotateCcw size={15} /><span><b>Reset</b></span>
            </button>
          </div>
          {(runs?.out || runs?.in) && (
            <div className="pl-steps">
              {runs.out && <Steps title="Fill" labels={STAGES_OUT} state={i => stageState(runs.out, i, outRunning)} note={i => stageNote('out', i)} />}
              {runs.in && <Steps title="Free" labels={STAGES_IN} state={i => stageState(runs.in, i, inRunning)} note={i => stageNote('in', i)} />}
              <button className="pl-steps-x" onClick={() => setRuns(null)} aria-label="Hide steps" title="Hide steps">✕</button>
            </div>
          )}
        </section>

        {/* ---- middle: the addresses ---- */}
        <section className="pl-panel pl-ips">
          <div className="pl-ips-head">
            <h2>IP addresses</h2>
            <div className="pl-stats">
              <Stat label="demo pods running" value={podsRunning} />
              <Stat label="addresses in use" value={inUse} />
              <Stat label="addresses given to nodes" value={given} />
              <Stat label="blocks" value={blocks.length} />
            </div>
          </div>
          {(pool?.spec?.cidrs ?? []).map((c: string) => <PoolLine key={c} cidr={c} blocks={blocks} colorOf={colorOf} phase={pool?.status?.phase ?? ''} message={pool?.status?.message ?? ''} />)}
          <div className="pl-nodes">
            {nodes.map(n => {
              const mine = blocks.filter(b => b.node === n.name);
              const cap = nodeCap(mine), used = mine.reduce((s, b) => s + b.used, 0);
              const reserved = new Set<number>();
              for (const r of aggregate(mine.map(b => (b.phase === 'Active' && !b.drain ? served(b) : { base: b.base, size: b.size })))) { reserved.add(r.base); reserved.add(r.base + 1); reserved.add(r.base + r.size - 1); }
              const first = startingOf(mine)?.name;
              const pct = cap ? Math.min(100, Math.round((100 * used) / cap)) : 0;
              return (
                <div key={n.name} className="pl-node" style={{ ['--node' as any]: colorOf(n.name) }}>
                  <div className="pl-node-head">
                    <span className="pl-node-name"><i />{n.name}</span>
                    <span className="pl-muted">{n.ip}</span>
                    <PodCount node={n.name} running={pods.filter(p => p.workload === demoName(n.name) && !p.terminating).length}
                      disabled={!ready || busy} onSet={setNodePods} />
                    <span className="pl-node-use"><b>{used}</b> of {cap} in use</span>
                    <span className="pl-meter"><span style={{ width: `${pct}%` }} className={pct >= Number(pool?.spec?.expandThresholdPercent ?? 80) ? 'high' : ''} /></span>
                  </div>
                  <div className="pl-blocks">
                    {mergedView(mine).map(b => (
                      <BlockView key={b.name} b={b} podByIp={podByIp} reserved={reserved} first={first} stopping={stopping}
                        onStop={ready ? stopPod : undefined} onStart={ready ? startPod : undefined}
                        isNew={(b.members ?? [b]).some(m => (firstSeen.current.get(m.name) ?? 0) > now - 8000)} />
                    ))}
                    {!mine.length && <span className="pl-muted">No block yet.</span>}
                  </div>
                </div>
              );
            })}
          </div>
        </section>

        {/* ---- right: what is happening ---- */}
        <section className="pl-panel pl-logpanel">
          <div className="pl-log-head">
            <h2>What is happening</h2>
            <button className="pl-clear" onClick={() => setLog([])}><Eraser size={13} /> Clear</button>
          </div>
          <LogView lines={log} nodes={nodes.map(n => n.name)} />
        </section>
      </main>
    </div>
  );
}

function Pill({ ok, label, value }: { ok: boolean; label: string; value: string }) {
  return <span className={`pl-pill ${ok ? 'ok' : 'bad'}`}><i />{label}: <b>{value}</b></span>;
}

// The big pool as a line. A /16 is 65,536 addresses and a block is 64, so on the full line a block is a
// sliver; the second line zooms into the part of the pool the nodes are using, at true scale.
function PoolLine({ cidr, blocks, colorOf, phase, message }: { cidr: string; blocks: Blk[]; colorOf: (node: string) => string; phase: string; message: string }) {
  const p = parseCidr(cidr);
  const mine = blocks.filter(b => b.base >= p.base && b.base + b.size <= p.base + p.size);
  const held = mine.reduce((s, b) => s + b.size, 0);
  let z = { base: p.base, size: Math.min(p.size, 256) };
  if (mine.length) {
    const lo = Math.min(...mine.map(b => b.base)), hi = Math.max(...mine.map(b => b.base + b.size)) - 1;
    let s = 256;
    while (Math.floor(lo / s) !== Math.floor(hi / s) && s < p.size) s *= 2;
    z = { base: Math.floor(lo / s) * s, size: s };
  }
  const pct = (b: number, size: number, of: { base: number; size: number }) => ({ left: `${(100 * (b - of.base)) / of.size}%`, width: `${(100 * size) / of.size}%` });
  const ticks = z.size > 256 ? Array.from({ length: z.size / 256 + 1 }, (_, i) => z.base + i * 256) : [z.base, z.base + z.size];
  return (
    <div className="pl-pool">
      <div className="pl-pool-row">
        <span className="pl-pool-label">Pool <b>{cidr}</b> <span className="pl-muted">{p.size.toLocaleString()} addresses · {held.toLocaleString()} given to nodes · {(p.size - held).toLocaleString()} free</span></span>
        {phase && <span className={`pl-pool-phase ${phase}`}>{phase === 'Exhausted' ? 'Full' : phase === 'Degraded' ? 'Almost full' : phase === 'Ready' ? 'Has room' : phase}</span>}
      </div>
      {phase === 'Exhausted' && <div className="pl-pool-msg">{message}</div>}
      <div className="pl-pool-line zoomed">
        {mergedView(mine).filter(b => b.base >= z.base && b.base < z.base + z.size).map(b => (
          <span key={b.name} className={`seg ph-${phaseOf(b)}`} style={{ ...pct(b.base, b.size, z), background: colorOf(b.node) }}
            title={`${b.cidr} → ${b.node} (${phaseOf(b)})`}>
            <em>{b.cidr.split('.').slice(2).join('.')}</em>
          </span>
        ))}
      </div>
      <div className="pl-pool-scale zoomed">
        {ticks.map((t, i) => <span key={t} style={{ left: `${(100 * (t - z.base)) / z.size}%` }} className={i === 0 ? 'first' : i === ticks.length - 1 ? 'last' : ''}>{numToIp(i === ticks.length - 1 ? t - 1 : t)}</span>)}
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return <div className="pl-stat"><b>{value}</b><span>{label}</span></div>;
}

function Steps({ title, labels, state, note }: { title: string; labels: string[]; state: (i: number) => string; note: (i: number) => string }) {
  return (
    <div className="pl-stepgroup">
      <div className="pl-steptitle">{title}</div>
      {labels.map((l, i) => {
        const s = state(i);
        return (
          <div key={l} className={`pl-step ${s}`}>
            {s === 'done' ? <CheckCircle2 size={15} /> : s === 'now' ? <Loader2 size={15} className="pl-spin" /> : <Circle size={15} />}
            <span>{l}</span>
            <em>{note(i)}</em>
          </div>
        );
      })}
    </div>
  );
}

const PHASE_LABEL: Record<string, string> = {
  Pending: 'new · waiting for routes', Active: 'active', Draining: 'being taken back', Quarantined: 'empty · resting',
};

function BlockView({ b, podByIp, reserved, isNew, first, stopping, onStop, onStart }: {
  b: Blk; podByIp: Map<string, PodInfo>; reserved: Set<number>; isNew: boolean; first?: string;
  stopping: Map<string, number>; onStop?: (p: PodInfo) => void; onStart?: (node: string) => void;
}) {
  const ph = phaseOf(b);
  const isStart = !!first && (b.name === first || !!b.members?.some(m => m.name === first));
  // The node counts leases (b.used). A pod that is still starting already holds its address but has not
  // reported it yet, so those addresses are shown as taken, in the order host-local hands them out.
  let known = 0;
  for (let i = 0; i < b.size; i++) if (!reserved.has(b.base + i) && podByIp.has(numToIp(b.base + i))) known++;
  let starting = Math.max(0, b.used - known);
  const leaving = b.release ? parseCidr(b.release) : null;
  const cells = Array.from({ length: b.size }, (_, i) => {
    const n = b.base + i;
    const ip = numToIp(n);
    if (leaving && n >= leaving.base && n < leaving.base + leaving.size && !podByIp.has(ip)) return <i key={i} className="leaving" title={`${ip} being given back`} />;
    if (reserved.has(n)) return <i key={i} className="rsv" title={`${ip} reserved`} />;
    const p = podByIp.get(ip);
    if (p) {
      if (p.app !== 'ipam-demo' || !onStop) return <i key={i} className={p.app === 'ipam-demo' ? 'on' : 'on other'} title={`${ip} → ${p.ns}/${p.name}`} />;
      if (stopping.has(ip)) return <i key={i} className="on stopping" title={`${ip}: stopping ${p.name}`} />;
      return <Cell key={i} cls="on pod" title={`${ip} → ${p.name}. Click to stop this pod.`} act={() => onStop(p)} />;
    }
    if (starting > 0) { starting--; return <i key={i} className="on starting" title="address taken, pod still starting" />; }
    if (onStart && ph === 'Active') return <Cell key={i} cls="free" title={`${ip} is free. Click to start a pod on ${b.node} (it takes the node's next free address).`} act={() => onStart(b.node)} />;
    return <i key={i} title={`${ip} free`} />;
  });
  return (
    <div className={`pl-block ph-${ph}${isNew ? ' isnew' : ''}`}>
      <div className="pl-block-top">
        <span className="pl-cidr">{b.cidr}</span>
        <span className="pl-tags">
          {isStart && <span className="pl-tag start" title="The node's starting block: never taken back">starting</span>}
          {b.release && <span className="pl-tag ph-Draining" title={`${b.release} is being given back`}>giving back half</span>}
          <span className={`pl-tag ph-${ph}`}>{PHASE_LABEL[ph] ?? ph}</span>
        </span>
      </div>
      <div className="pl-cells">{cells}</div>

    </div>
  );
}

// The Deployment that pins demo pods to one node (same name the dev server uses).
const demoName = (node: string) => `web-${node.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 60);

// A number box and Set button: run exactly this many demo pods on the node.
function PodCount({ node, running, disabled, onSet }: { node: string; running: number; disabled: boolean; onSet: (node: string, n: number) => void }) {
  const [value, setValue] = useState('');
  const n = Number(value);
  const valid = value !== '' && Number.isInteger(n) && n >= 0 && n <= 300;
  const submit = () => { if (valid && !disabled) { onSet(node, n); setValue(''); } };
  return (
    <span className="pl-podcount" title={`Demo pods pinned to ${node}`}>
      <input type="number" min={0} max={300} placeholder={String(running)} value={value} aria-label={`demo pods on ${node}`}
        onChange={e => setValue(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') submit(); }} />
      <span className="pl-muted">pods</span>
      <button onClick={submit} disabled={!valid || disabled}>Set</button>
    </span>
  );
}

// A square that acts on one click (or Enter/Space).
function Cell({ cls, title, act }: { cls: string; title: string; act: () => void }) {
  return (
    <i role="button" tabIndex={0} className={`${cls} act`} title={title} onClick={act}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); act(); } }} />
  );
}

const WHO_LABEL: Record<Who, string> = { you: 'you', kubernetes: 'kubernetes', controller: 'controller', agent: 'agent', node: 'node' };

// IPs, CIDRs and node names stand out from the sentence around them.
function Rich({ text, nodes }: { text: string; nodes: string[] }) {
  const names = [...nodes].filter(Boolean).sort((a, b) => b.length - a.length).map(n => n.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|');
  const re = new RegExp(`(\\d{1,3}(?:\\.\\d{1,3}){3}(?:/\\d{1,2})?${names ? `|${names}` : ''})`, 'g');
  return <>{text.split(re).map((part, i) => (i % 2 === 1 ? <b key={i} className={/^\d/.test(part) ? 'ip' : 'nd'}>{part}</b> : part))}</>;
}

function LogView({ lines, nodes }: { lines: LogLine[]; nodes: string[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  useEffect(() => { if (ref.current && stick.current) ref.current.scrollTop = ref.current.scrollHeight; }, [lines.length]);
  return (
    <div className="pl-term" ref={ref} onScroll={e => { const el = e.currentTarget; stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40; }}>
      {!lines.length && <div className="pl-tl dim">waiting for events… press Fill the IP pool</div>}
      {lines.map((l, i) => (
        <div key={l.id} className={`pl-tl ${l.tone}${i > 0 && l.t - lines[i - 1].t > 4000 ? ' gap' : ''}`}>
          <span className="t">{clock(l.t)}</span>
          <span className={`w ${l.who}`}>{WHO_LABEL[l.who]}</span>
          <span className="m"><Rich text={l.text} nodes={nodes} />{l.detail && <span className="cmd">$ {l.detail}</span>}</span>
        </div>
      ))}
      <div className="pl-tl"><span /><span /><span className="pl-cursor" /></div>
    </div>
  );
}

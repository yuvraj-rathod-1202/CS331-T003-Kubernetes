// In-browser port of the buddy IPAM planner (operator/internal/controller/ipam).
// Same rules and default parameters, scaled in time so a demo is watchable.
// It is a simulation of the controller's decisions, not a connection to a cluster.

export type Phase = 'Pending' | 'Active' | 'Draining' | 'Quarantined';

export interface Block {
  id: string;
  base: number; // in addresses, relative to the pool start
  size: number;
  node: string;
  phase: Phase;
  since: number; // sim seconds
  used: number;
  drain: boolean;
}

export interface NodeState {
  name: string;
  maxPods: number;
  demand: number;
  unmet: number;
  starvedAt: number; // sim seconds, -1 for never
}

export interface Params {
  poolSize: number; // addresses, power of two
  minBlock: number;
  maxBlock: number;
  expandThreshold: number;
  triggerFloor: number;
  growthPercent: number;
  capacityCap: number;
  initialPercent: number;
  shrinkThreshold: number;
  shrinkCooldown: number; // seconds
  quarantine: number; // seconds
  promoteDelay: number; // seconds: route install + acks on every node
  drainSettle: number;
  podLifetime: number; // seconds
}

export const defaultParams: Params = {
  poolSize: 4096,
  minBlock: 16,
  maxBlock: 1024,
  expandThreshold: 80,
  triggerFloor: 8,
  growthPercent: 50,
  capacityCap: 120,
  initialPercent: 25,
  shrinkThreshold: 50,
  shrinkCooldown: 60,
  quarantine: 20,
  promoteDelay: 5,
  drainSettle: 5,
  podLifetime: 120,
};

export const CELL = 16;
const OVERHEAD = 3;
export const STEP = 1; // seconds per tick

export const usable = (size: number) => (size <= OVERHEAD ? 0 : size - OVERHEAD);
const nextPow2 = (n: number) => { let p = 1; while (p < n) p <<= 1; return p; };

export function cidrOf(base: number, size: number): string {
  const ip = (10 << 24) | (244 << 16) | base;
  const o = [(ip >>> 24) & 255, (ip >>> 16) & 255, (ip >>> 8) & 255, ip & 255];
  return `${o.join('.')}/${32 - Math.log2(size)}`;
}

// Merge buddies: two blocks that are halves of one aligned block become one range.
export function aggregate(blocks: { base: number; size: number }[]): { base: number; size: number }[] {
  const set = new Map<string, { base: number; size: number }>();
  const key = (b: { base: number; size: number }) => `${b.base}/${b.size}`;
  blocks.forEach(b => set.set(key(b), { base: b.base, size: b.size }));
  for (let changed = true; changed;) {
    changed = false;
    for (const b of set.values()) {
      const sib = { base: b.base ^ b.size, size: b.size };
      if (set.has(key(sib))) {
        set.delete(key(b));
        set.delete(key(sib));
        const m = { base: Math.min(b.base, sib.base), size: b.size * 2 };
        set.set(key(m), m);
        changed = true;
        break;
      }
    }
  }
  return [...set.values()].sort((a, b) => a.base - b.base);
}

export const capacityOf = (blocks: { base: number; size: number }[]) =>
  aggregate(blocks).reduce((t, r) => t + usable(r.size), 0);

export interface LogLine { t: number; kind: 'info' | 'grow' | 'shrink' | 'warn' | 'ok'; text: string }

export class IpamSim {
  p: Params;
  t = 0;
  nodes: NodeState[] = [];
  blocks: Block[] = [];
  owner: Int32Array; // per 16-address cell: index into blocks ids, -1 free
  log: LogLine[] = [];
  shrinkOn = true;
  emergencyOn = true;
  private seq = 0;
  private ids = new Map<number, string>();
  private rng: () => number;
  ops = 0;
  private warned = new Set<string>();

  constructor(p: Params = defaultParams, seed = 7) {
    this.p = p;
    this.owner = new Int32Array(p.poolSize / CELL).fill(-1);
    let s = seed;
    this.rng = () => { s = (s * 1664525 + 1013904223) >>> 0; return s / 2 ** 32; };
  }

  // ---- buddy pool over cells -------------------------------------------------
  private cellsFree(base: number, size: number) {
    for (let c = base / CELL; c < (base + size) / CELL; c++) if (this.owner[c] !== -1) return false;
    return true;
  }
  private maximalFree(size: number): number[] {
    const out: number[] = [];
    for (let b = 0; b < this.p.poolSize; b += size) {
      if (!this.cellsFree(b, size)) continue;
      const parentFree = size < this.p.poolSize && this.cellsFree(b & ~(size * 2 - 1), size * 2);
      if (!parentFree) out.push(b);
    }
    return out;
  }
  private take(base: number, size: number, id: number) {
    for (let c = base / CELL; c < (base + size) / CELL; c++) this.owner[c] = id;
  }
  /** Plain best fit: an exact-size free block first, else split the smallest larger one. */
  private allocPlain(size: number): number | null {
    const exact = this.maximalFree(size);
    if (exact.length) return exact[0];
    for (let s = size * 2; s <= this.p.poolSize; s *= 2) {
      const f = this.maximalFree(s);
      if (f.length) return f[0];
    }
    return null;
  }
  /** First blocks: split a larger free block so the buddy stays free. */
  private allocSpread(size: number): number | null {
    for (let s = size * 2; s <= this.p.poolSize; s *= 2) {
      const f = this.maximalFree(s);
      if (f.length) return f[0];
    }
    return this.allocPlain(size);
  }

  private addBlock(node: string, base: number, size: number, why: string): Block {
    const id = this.seq++;
    const b: Block = { id: `b${id}`, base, size, node, phase: 'Pending', since: this.t, used: 0, drain: false };
    this.blocks.push(b);
    this.take(base, size, id);
    this.ids.set(id, b.id);
    this.ops++;
    this.say('grow', `${why}: ${cidrOf(base, size)} → ${node} (Pending: routing to other nodes)`);
    return b;
  }
  private removeBlock(b: Block) {
    const n = Number(b.id.slice(1));
    for (let c = b.base / CELL; c < (b.base + b.size) / CELL; c++) if (this.owner[c] === n) this.owner[c] = -1;
    this.blocks = this.blocks.filter(x => x !== b);
  }
  say(kind: LogLine['kind'], text: string) {
    this.log.push({ t: this.t, kind, text });
    if (this.log.length > 300) this.log.shift();
  }

  // ---- cluster operations ----------------------------------------------------
  addNode(maxPods = 110) {
    const name = `node-${this.nodes.length ? Math.max(...this.nodes.map(n => Number(n.name.slice(5)))) + 1 : 1}`;
    this.nodes.push({ name, maxPods, demand: 10, unmet: 0, starvedAt: -1 });
    this.say('info', `${name} joined (max-pods ${maxPods})`);
    return name;
  }
  removeNode(name: string) {
    this.nodes = this.nodes.filter(n => n.name !== name);
    this.say('warn', `${name} deleted: its blocks are withdrawn and quarantined`);
  }
  setDemand(name: string, pods: number) {
    const n = this.nodes.find(x => x.name === name);
    if (n) n.demand = Math.max(0, Math.min(pods, n.maxPods * 2));
  }
  blocksOf = (node: string) => this.blocks.filter(b => b.node === node);

  // ---- one tick --------------------------------------------------------------
  tick() {
    this.t += STEP;
    this.pods();
    this.agents();
    this.controller();
  }

  private groupsOf(node: string) {
    const place = this.blocksOf(node).filter(b => b.phase === 'Active' && !b.drain);
    return aggregate(place).map(r => ({ r, members: place.filter(b => b.base >= r.base && b.base < r.base + r.size) }));
  }

  private pods() {
    const death = 1 - Math.exp(-STEP / this.p.podLifetime);
    for (const b of this.blocks) {
      let k = 0;
      for (let i = 0; i < b.used; i++) if (this.rng() < death) k++;
      b.used -= k;
    }
    for (const n of this.nodes) {
      const have = this.blocksOf(n.name).reduce((t, b) => t + b.used, 0);
      n.unmet = 0;
      if (n.demand < have) {
        let extra = have - n.demand;
        const bs = this.blocksOf(n.name).filter(b => b.used > 0);
        while (extra > 0 && bs.length) {
          const b = bs[Math.floor(this.rng() * bs.length)];
          b.used--; extra--;
          if (b.used === 0) bs.splice(bs.indexOf(b), 1);
        }
      } else if (n.demand > have) {
        let want = n.demand - have;
        const groups = this.groupsOf(n.name);
        const free = groups.map(g => Math.max(0, usable(g.r.size) - g.members.reduce((t, b) => t + b.used, 0)));
        while (want > 0) {
          const total = free.reduce((a, b) => a + b, 0);
          if (total <= 0) break;
          let r = this.rng() * total, i = 0;
          while (r >= free[i]) { r -= free[i]; i++; }
          const m = groups[i].members;
          m[Math.floor(this.rng() * m.length)].used++;
          free[i]--; want--;
        }
        n.unmet = want;
        if (want > 0 && this.emergencyOn) n.starvedAt = this.t;
      }
    }
  }

  private agents() {
    for (const b of this.blocks) {
      const since = this.t - b.since;
      const set = (ph: Phase, msg?: string) => { b.phase = ph; b.since = this.t; if (msg) this.say('info', msg); };
      if (b.phase === 'Pending' && since >= this.p.promoteDelay) set('Active', `${cidrOf(b.base, b.size)} Active on ${b.node} (routes acked, CNI config written)`);
      else if (b.phase === 'Active' && b.drain) set('Draining');
      else if (b.phase === 'Draining' && !b.drain) set('Active', `${cidrOf(b.base, b.size)} restored on ${b.node}`);
      else if (b.phase === 'Draining' && b.used === 0 && since >= this.p.drainSettle) set('Quarantined', `${cidrOf(b.base, b.size)} empty: routes withdrawn, held for ${this.p.quarantine}s`);
    }
  }

  private initialSize(maxPods: number) {
    return Math.min(this.p.maxBlock, nextPow2(Math.max(16, Math.ceil(maxPods * this.p.initialPercent / 100))));
  }

  private controller() {
    const names = new Set(this.nodes.map(n => n.name));
    for (const b of [...this.blocks]) {
      if (b.phase === 'Quarantined' && this.t - b.since >= this.p.quarantine) {
        this.removeBlock(b);
        this.ops++;
        this.say('shrink', `${cidrOf(b.base, b.size)} released to the pool`);
      } else if (!names.has(b.node) && b.phase !== 'Quarantined') {
        b.phase = 'Quarantined'; b.since = this.t; b.used = 0;
      }
    }
    for (const n of [...this.nodes].sort((a, b) => a.name.localeCompare(b.name))) this.planNode(n);
  }

  private planNode(n: NodeState) {
    const owned = this.blocksOf(n.name);
    if (!owned.length) {
      const size = this.initialSize(n.maxPods);
      const base = this.allocSpread(size);
      if (base === null) { this.say('warn', `no initial block for ${n.name}: pool exhausted`); return; }
      this.addBlock(n.name, base, size, 'initial');
      return;
    }
    if (owned.some(b => b.phase === 'Pending' || (b.phase === 'Draining' && !b.drain))) return;

    const active = owned.filter(b => b.phase === 'Active' && !b.drain);
    const draining = owned.filter(b => b.drain && (b.phase === 'Active' || b.phase === 'Draining'));
    const capacity = capacityOf(active);
    const used = active.reduce((t, b) => t + b.used, 0);
    const latest = Math.max(...owned.map(b => b.since));
    // A failure counts for 3 minutes, and only if no block changed since: new space is the answer to it.
    const starved = n.starvedAt > latest && this.t - n.starvedAt <= 180;
    const trigger = Math.max(this.p.triggerFloor, Math.floor(capacity * (100 - this.p.expandThreshold) / 100));

    if (capacity - used < trigger || starved) {
      if (draining.length) {
        const d = [...draining].sort((a, b) => b.used - a.used)[0];
        d.drain = false;
        this.say('grow', `${cidrOf(d.base, d.size)} restored on ${n.name}: it needs the space back`);
        return;
      }
      this.grow(n, active, capacity, starved);
      return;
    }
    if (this.shrinkOn) this.shrink(n, active, used + draining.reduce((t, b) => t + b.used, 0));
  }

  private grow(n: NodeState, active: Block[], capacity: number, starved: boolean) {
    const headroom = Math.floor(n.maxPods * this.p.capacityCap / 100) - capacity;
    let target = nextPow2(Math.max(Math.ceil(capacity * this.p.growthPercent / 100), this.p.minBlock));
    while (target > this.p.minBlock && usable(target) > headroom) target >>= 1;
    if (usable(target) > headroom) {
      if (!this.warned.has(n.name)) {
        this.warned.add(n.name);
        this.say('warn', `${n.name} reached its capacity cap (${this.p.capacityCap}% of ${n.maxPods} pods)`);
      }
      return;
    }
    this.warned.delete(n.name);
    const why = starved ? 'emergency grow (kubelet: no IP addresses available)' : 'grow';

    // The node's own free buddy first, even if it overshoots by up to 2x: the halves merge into one range.
    for (const b of [...active].sort((a, c) => c.size - a.size)) {
      if (b.size > 2 * target || usable(b.size) > headroom || b.size >= this.p.maxBlock) continue;
      const bb = b.base ^ b.size;
      if (this.cellsFree(bb, b.size)) { this.addBlock(n.name, bb, b.size, `${why}, in place`); return; }
    }
    const base = this.allocPlain(target);
    if (base === null) { this.say('warn', `cannot grow ${n.name}: pool exhausted`); return; }
    this.addBlock(n.name, base, target, why);
  }

  private shrink(n: NodeState, active: Block[], demand: number) {
    if (active.length < 2) return;
    const floor = usable(this.initialSize(n.maxPods));
    const cands = [...active].sort((a, b) => a.used - b.used || a.size - b.size || a.base - b.base);
    for (const c of cands) {
      if (this.t - c.since < this.p.shrinkCooldown) continue;
      const rest = capacityOf(active.filter(b => b !== c));
      const trigger = Math.max(this.p.triggerFloor, Math.floor(rest * (100 - this.p.expandThreshold) / 100));
      if (rest < floor || demand * 100 > this.p.shrinkThreshold * rest || rest - demand < trigger) continue;
      c.drain = true;
      this.ops++;
      this.say('shrink', `draining ${cidrOf(c.base, c.size)} on ${n.name} (rest still ≤ ${this.p.shrinkThreshold}% used)`);
      return;
    }
  }

  // ---- read-outs -------------------------------------------------------------
  stats() {
    const allocated = this.blocks.reduce((t, b) => t + b.size, 0);
    const pods = this.blocks.reduce((t, b) => t + b.used, 0);
    const wanted = this.nodes.reduce((t, n) => t + n.demand, 0);
    const unmet = this.nodes.reduce((t, n) => t + n.unmet, 0);
    return { allocated, pods, wanted, unmet, poolPct: (100 * allocated) / this.p.poolSize, ops: this.ops };
  }
}

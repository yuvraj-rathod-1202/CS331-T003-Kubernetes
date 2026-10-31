import { useEffect, useRef, useState } from 'react';
import { Boxes, Play, Pause, RotateCcw, Plus, Trash2, Zap, Moon, Flame, ScrollText, BarChart3, BookOpen } from 'lucide-react';
import { IpamSim, CELL, cidrOf, capacityOf, usable, defaultParams } from '../ipam/engine';
import type { Block } from '../ipam/engine';
import { policies, scenarios, sweep, liveChecks } from '../ipam/evalData';
import './Project1Demo.css';

const NODE_COLORS = ['#3b82f6', '#f97316', '#10b981', '#e879a9', '#8b5cf6', '#eab308', '#14b8a6', '#ef4444'];
const POLICY_COLORS = ['#3b82f6', '#f97316', '#10b981', '#e879a9'];

function makeSim(nodes = 3) {
  const s = new IpamSim(defaultParams);
  for (let i = 0; i < nodes; i++) s.addNode();
  for (let i = 0; i < 12; i++) s.tick();
  // /project1?preset=burst starts mid-burst, for screenshots and quick demos.
  if (new URLSearchParams(window.location.search).get('preset') === 'burst') {
    s.nodes.forEach(n => s.setDemand(n.name, 200));
    for (let i = 0; i < 90; i++) s.tick();
  }
  s.log.length = 0;
  s.say('info', 'Started with 3 nodes, 4096-address pool (10.244.0.0/20), blocks of 16+ addresses');
  return s;
}

export function Project1Demo() {
  const sim = useRef<IpamSim>(makeSim());
  const [, setFrame] = useState(0);
  const [running, setRunning] = useState(true);
  const [speed, setSpeed] = useState(4);
  const [tab, setTab] = useState<'log' | 'eval' | 'how'>(() => {
    const t = new URLSearchParams(window.location.search).get('tab');
    return t === 'eval' || t === 'how' ? t : 'log';
  });
  const [hover, setHover] = useState<string | null>(null);
  const [shrinkOn, setShrinkOn] = useState(true);
  const [emergencyOn, setEmergencyOn] = useState(true);

  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => {
      for (let i = 0; i < speed; i++) sim.current.tick();
      setFrame(f => f + 1);
    }, 250);
    return () => clearInterval(id);
  }, [running, speed]);

  const s = sim.current;
  const stats = s.stats();
  const refresh = () => setFrame(f => f + 1);

  const colorOf = new Map<string, string>();
  s.nodes.forEach((n, i) => colorOf.set(n.name, NODE_COLORS[i % NODE_COLORS.length]));

  // Cheap (256 cells), so recomputed every frame.
  const cells: (Block | null)[] = new Array(s.p.poolSize / CELL).fill(null);
  s.blocks.forEach(b => {
    for (let c = b.base / CELL; c < (b.base + b.size) / CELL; c++) cells[c] = b;
  });

  const reset = () => {
    sim.current = makeSim();
    sim.current.shrinkOn = shrinkOn;
    sim.current.emergencyOn = emergencyOn;
    refresh();
  };
  const all = (pods: number, msg: string) => {
    s.nodes.forEach(n => s.setDemand(n.name, pods));
    s.say('info', msg);
    refresh();
  };
  const skew = () => {
    s.nodes.forEach((n, i) => s.setDemand(n.name, i === 0 ? 220 : 25));
    s.say('info', `Skew: ${s.nodes[0]?.name} → 220 pods, the others → 25`);
    refresh();
  };

  const served = stats.wanted ? Math.min(100, (100 * stats.pods) / stats.wanted) : 100;

  return (
    <div className="p1-container">
      <header className="p1-header">
        <div className="p1-title-group">
          <span className="p1-badge">P1</span>
          <div>
            <div className="p1-title"><Boxes size={18} /> Buddy-style dynamic pod IPAM</div>
            <div className="p1-subtitle">
              Nodes get address blocks from a pool, grow when they run low, give space back when idle.
              <b> Simulation</b> of the operator's planner (same rules and defaults), not a live cluster.
            </div>
          </div>
        </div>
        <div className="p1-metrics">
          <div className="p1-pill"><span className="label">Pods served</span><span className={`val ${served < 99 ? 'warn' : 'good'}`}>{stats.pods}/{stats.wanted}</span></div>
          <div className="p1-pill"><span className="label">Pool used</span><span className="val">{stats.poolPct.toFixed(1)}%</span></div>
          <div className="p1-pill"><span className="label">Block changes</span><span className="val">{stats.ops}</span></div>
          <div className="p1-pill"><span className="label">Sim time</span><span className="val">{Math.floor(s.t / 60)}m{String(s.t % 60).padStart(2, '0')}s</span></div>
          <a className="p1-link" href="/">Cluster view</a>
          <a className="p1-link" href="/project2">Problem 2</a>
        </div>
      </header>

      <div className="p1-toolbar">
        <button onClick={() => setRunning(r => !r)}>{running ? <Pause size={14} /> : <Play size={14} />}{running ? 'Pause' : 'Play'}</button>
        <select value={speed} onChange={e => setSpeed(Number(e.target.value))} aria-label="speed">
          <option value={1}>4× real time</option>
          <option value={4}>16× real time</option>
          <option value={16}>64× real time</option>
        </select>
        <span className="sep" />
        <button onClick={() => all(200, 'Burst: every node → 200 pods')}><Flame size={14} />Burst</button>
        <button onClick={() => all(15, 'Calm: every node → 15 pods')}><Moon size={14} />Calm</button>
        <button onClick={skew}><Zap size={14} />Skew</button>
        <button onClick={() => { s.addNode(); refresh(); }}><Plus size={14} />Add node</button>
        <span className="sep" />
        <label className="toggle"><input type="checkbox" checked={shrinkOn} onChange={e => { setShrinkOn(e.target.checked); s.shrinkOn = e.target.checked; s.say('info', `Shrink ${e.target.checked ? 'on' : 'off'}`); }} />Shrink (drain idle blocks)</label>
        <label className="toggle"><input type="checkbox" checked={emergencyOn} onChange={e => { setEmergencyOn(e.target.checked); s.emergencyOn = e.target.checked; s.say('info', `Emergency grow ${e.target.checked ? 'on' : 'off'}`); }} />Emergency grow</label>
        <span className="sep" />
        <button onClick={reset}><RotateCcw size={14} />Reset</button>
      </div>

      <div className="p1-main">
        <section className="p1-panel">
          <div className="p1-panel-title">Address pool 10.244.0.0/20 <span>one square = {CELL} addresses; colour = owning node</span></div>
          <div className="p1-grid" role="img" aria-label="Pool address map">
            {cells.map((b, i) => {
              const color = b ? colorOf.get(b.node) ?? '#64748b' : undefined;
              const cls = b ? `p1-cell ph-${b.phase}${b.drain ? ' drain' : ''}${hover === b.id ? ' hl' : ''}` : 'p1-cell free';
              return (
                <div
                  key={i}
                  className={cls}
                  style={b ? { ['--c' as string]: color } : undefined}
                  title={b ? `${cidrOf(b.base, b.size)} → ${b.node} (${b.phase}${b.drain ? ', draining' : ''}, ${b.used} used)` : `free: ${cidrOf(i * CELL, CELL)}`}
                  onMouseEnter={() => b && setHover(b.id)}
                  onMouseLeave={() => setHover(null)}
                />
              );
            })}
          </div>
          <div className="p1-legend">
            <span><i className="p1-cell free" /> free</span>
            <span><i className="p1-cell ph-Active" style={{ ['--c' as string]: '#94a3b8' }} /> Active</span>
            <span><i className="p1-cell ph-Pending" style={{ ['--c' as string]: '#94a3b8' }} /> Pending (routing)</span>
            <span><i className="p1-cell ph-Draining drain" style={{ ['--c' as string]: '#94a3b8' }} /> Draining</span>
            <span><i className="p1-cell ph-Quarantined" style={{ ['--c' as string]: '#94a3b8' }} /> Quarantined</span>
          </div>
          <p className="p1-hint">
            Each node's first block leaves its buddy free, so the node can double in place. Two buddy blocks are served as
            one range, which pays the 3 reserved addresses once (a /26 holds 61 pods, two /27s 58).
          </p>
        </section>

        <section className="p1-panel p1-nodes">
          <div className="p1-panel-title">Nodes <span>drag a slider to change how many pods a node should run</span></div>
          {s.nodes.map(n => {
            const blks = s.blocksOf(n.name).sort((a, b) => a.base - b.base);
            const active = blks.filter(b => b.phase === 'Active' && !b.drain);
            const cap = capacityOf(active);
            const used = blks.reduce((t, b) => t + b.used, 0);
            const ranges = new Set(active.map(b => b.size)).size;
            return (
              <div key={n.name} className="p1-node" style={{ ['--c' as string]: colorOf.get(n.name) }}>
                <div className="p1-node-head">
                  <b>{n.name}</b>
                  <span className="muted">max-pods {n.maxPods}</span>
                  <span className={`p1-unmet ${n.unmet ? 'on' : ''}`}>{n.unmet ? `${n.unmet} pods waiting for an address` : 'all pods placed'}</span>
                  <button className="icon" title="Delete node" onClick={() => { s.removeNode(n.name); refresh(); }}><Trash2 size={13} /></button>
                </div>
                <div className="p1-slider">
                  <input type="range" min={0} max={n.maxPods * 2} value={n.demand} onChange={e => { s.setDemand(n.name, Number(e.target.value)); refresh(); }} />
                  <span>{n.demand} pods wanted</span>
                </div>
                <div className="p1-bar" title={`${used} used of ${cap} usable`}>
                  <div className="fill" style={{ width: `${cap ? Math.min(100, (100 * used) / cap) : 0}%` }} />
                  <span>{used} used / {cap} usable{ranges ? '' : ''}</span>
                </div>
                <div className="p1-chips">
                  {blks.map(b => (
                    <span key={b.id} className={`chip ph-${b.phase}${b.drain ? ' drain' : ''}`} onMouseEnter={() => setHover(b.id)} onMouseLeave={() => setHover(null)}>
                      {cidrOf(b.base, b.size)} · {b.drain && b.phase !== 'Quarantined' ? 'Draining' : b.phase} · {b.used}/{usable(b.size)}
                    </span>
                  ))}
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
        {tab === 'log' && <LogView lines={s.log} />}
        {tab === 'eval' && <EvalView />}
        {tab === 'how' && <HowView />}
      </section>
    </div>
  );
}

function LogView({ lines }: { lines: IpamSim['log'] }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (ref.current) ref.current.scrollTop = ref.current.scrollHeight; }, [lines.length]);
  return (
    <div className="p1-log" ref={ref}>
      {lines.map((l, i) => (
        <div key={i} className={`line ${l.kind}`}>
          <span className="t">{Math.floor(l.t / 60)}:{String(l.t % 60).padStart(2, '0')}</span>{l.text}
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
        <em>fixed /24 and /25 per node, Calico-style 64-address blocks, and this project (buddy)</em>
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
            Honest reading: buddy serves every pod the pool can hold and wastes far less than a fixed /24, but Calico-style
            blocks pack as well or better when load changes (wave 52% vs 41%, burst 38% vs 35%). Buddy's case is allocation
            in the background, off the pod-start path. Shrink is as slow as pod turnover. This is a model, not a cluster measurement.
          </p>
          <div className="p1-bars-title" style={{ marginTop: 14 }}>Verified on a 3-node minikube cluster</div>
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
        <h4>Things to try</h4>
        <ul>
          <li><b>Burst</b>, then watch blocks go Pending → Active and merge.</li>
          <li><b>Calm</b>, then watch blocks drain, quarantine and return to the pool.</li>
          <li><b>Skew</b>: one hot node grows without taking space from the others.</li>
          <li>Turn off <b>Emergency grow</b> and push demand past capacity.</li>
        </ul>
      </div>
    </div>
  );
}
